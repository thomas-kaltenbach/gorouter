package round_tripper

import (
	"context"
	"errors"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	"github.com/uber-go/zap"

	"code.cloudfoundry.org/gorouter/config"
	"code.cloudfoundry.org/gorouter/handlers"
	"code.cloudfoundry.org/gorouter/logger"
	"code.cloudfoundry.org/gorouter/metrics"
	"code.cloudfoundry.org/gorouter/proxy/fails"
	"code.cloudfoundry.org/gorouter/proxy/handler"
	"code.cloudfoundry.org/gorouter/proxy/utils"
	"code.cloudfoundry.org/gorouter/route"
)

const (
	VcapCookieId              = "__VCAP_ID__"
	CookieHeader              = "Set-Cookie"
	BadGatewayMessage         = "502 Bad Gateway: Registered endpoint failed to handle the request."
	HostnameErrorMessage      = "503 Service Unavailable"
	InvalidCertificateMessage = "526 Invalid SSL Certificate"
	SSLHandshakeMessage       = "525 SSL Handshake Failed"
	SSLCertRequiredMessage    = "496 SSL Certificate Required"
	ContextCancelledMessage   = "499 Request Cancelled"
)

//go:generate counterfeiter -o fakes/fake_proxy_round_tripper.go . ProxyRoundTripper
type ProxyRoundTripper interface {
	http.RoundTripper
	CancelRequest(*http.Request)
}

type RoundTripperFactory interface {
	New(expectedServerName string, isRouteService bool) ProxyRoundTripper
}

func GetRoundTripper(endpoint *route.Endpoint, roundTripperFactory RoundTripperFactory, isRouteService bool) ProxyRoundTripper {
	endpoint.RoundTripperInit.Do(func() {
		endpoint.SetRoundTripperIfNil(func() route.ProxyRoundTripper {
			return roundTripperFactory.New(endpoint.ServerCertDomainSAN, isRouteService)
		})
	})

	return endpoint.RoundTripper()
}

//go:generate counterfeiter -o fakes/fake_error_handler.go --fake-name ErrorHandler . errorHandler
type errorHandler interface {
	HandleError(utils.ProxyResponseWriter, error)
}

func NewProxyRoundTripper(
	roundTripperFactory RoundTripperFactory,
	retriableClassifiers fails.Classifier,
	logger logger.Logger,
	combinedReporter metrics.ProxyReporter,
	errHandler errorHandler,
	routeServicesTransport http.RoundTripper,
	cfg *config.Config,
) ProxyRoundTripper {

	return &roundTripper{
		logger:                   logger,
		defaultLoadBalance:       cfg.LoadBalance,
		combinedReporter:         combinedReporter,
		secureCookies:            cfg.SecureCookies,
		roundTripperFactory:      roundTripperFactory,
		retriableClassifier:      retriableClassifiers,
		errorHandler:             errHandler,
		routeServicesTransport:   routeServicesTransport,
		endpointTimeout:          cfg.EndpointTimeout,
		stickySessionCookieNames: cfg.StickySessionCookieNames,
	}
}

type roundTripper struct {
	logger                   logger.Logger
	defaultLoadBalance       string
	combinedReporter         metrics.ProxyReporter
	secureCookies            bool
	roundTripperFactory      RoundTripperFactory
	retriableClassifier      fails.Classifier
	errorHandler             errorHandler
	routeServicesTransport   http.RoundTripper
	endpointTimeout          time.Duration
	stickySessionCookieNames config.StringSet
}

func (rt *roundTripper) RoundTrip(originalRequest *http.Request) (*http.Response, error) {
	var err error

	request := originalRequest.Clone(originalRequest.Context())

	if request.Body != nil {
		// Temporarily disable closing of the body while in the RoundTrip function, since
		// the underlying Transport will close the client request body.
		// https://github.com/golang/go/blob/ab5d9f5831cd267e0d8e8954cfe9987b737aec9c/src/net/http/request.go#L179-L182

		request.Body = ioutil.NopCloser(request.Body)
	}

	reqInfo, err := handlers.ContextRequestInfo(request)
	if err != nil {
		return nil, err
	}
	if reqInfo.RoutePool == nil {
		return nil, errors.New("RoutePool not set on context")
	}

	if reqInfo.ProxyResponseWriter == nil {
		return nil, errors.New("ProxyResponseWriter not set on context")
	}

	endpoint, res, stickyEndpointID, err := rt.doRoundTripWithRetries(reqInfo, request)

	reqInfo.StoppedAt = time.Now()

	if err != nil {
		rt.errorHandler.HandleError(reqInfo.ProxyResponseWriter, err)
		return nil, err
	}

	if res != nil && endpoint.PrivateInstanceId != "" {
		setupStickySession(
			res, endpoint, stickyEndpointID, rt.secureCookies,
			reqInfo.RoutePool.ContextPath(), rt.stickySessionCookieNames,
		)
	}

	return res, nil
}

func (rt *roundTripper) doRoundTripWithRetries(reqInfo *handlers.RequestInfo, request *http.Request) (*route.Endpoint, *http.Response, string, error) {
	logger := rt.logger
	var err error
	var res *http.Response
	var endpoint *route.Endpoint

	stickyEndpointID := getStickySession(request, rt.stickySessionCookieNames)
	iter := reqInfo.RoutePool.Endpoints(rt.defaultLoadBalance, stickyEndpointID)

	logger = rt.logger

	if reqInfo.RouteServiceURL == nil {
		for attempt := 1; attempt < handler.MaxRetries+1; attempt++ {
			endpoint, res, logger, err = rt.doRoundTripToBackend(iter, request, reqInfo, attempt)

			rt.logBackendEndpointFailedError(err, attempt, request, logger)

			if rt.shouldBeRetried(err) {
				logger.Debug("retriable-error", zap.Object("error", err))
			} else {
				break
			}
		}
	} else {
		for retry := 0; retry < handler.MaxRetries; retry++ {
			logger.Debug(
				"route-service",
				zap.Object("route-service-url", reqInfo.RouteServiceURL),
				zap.Int("attempt", retry),
			)

			endpoint = &route.Endpoint{
				Tags: map[string]string{},
			}
			reqInfo.RouteEndpoint = endpoint
			request.Host = reqInfo.RouteServiceURL.Host
			request.URL = new(url.URL)
			*request.URL = *reqInfo.RouteServiceURL

			var roundTripper http.RoundTripper
			roundTripper = GetRoundTripper(endpoint, rt.roundTripperFactory, true)
			if reqInfo.ShouldRouteToInternalRouteService {
				roundTripper = rt.routeServicesTransport
			}

			res, err = rt.timedRoundTrip(roundTripper, request)
			if err != nil {
				logger.Error("route-service-connection-failed", zap.Error(err))

				if !rt.retriableClassifier.Classify(err) {
					break
				}

			} else if res != nil {
				if !wasRequestSuccessful(res) {
					logger.Info(
						"route-service-response",
						zap.String("endpoint", request.URL.String()),
						zap.Int("status-code", res.StatusCode),
					)
				}
				break
			}
		}
	}

	if see, ok := err.(*selectEndpointError); ok {
		err = see.err
	}
	return endpoint, res, stickyEndpointID, err
}

func wasRequestSuccessful(res *http.Response) bool {
	return 200 <= res.StatusCode && res.StatusCode < 300
}

func (rt *roundTripper) logBackendEndpointFailedError(err error, attempt int, request *http.Request, logger logger.Logger) {
	if err != nil {
		_, isSelectEndpointError := err.(*selectEndpointError)
		if !isSelectEndpointError {
			logger.Error(
				"backend-endpoint-failed",
				zap.Error(err),
				zap.Int("attempt", attempt),
				zap.String("vcap_request_id", request.Header.Get(handlers.VcapRequestIdHeader)),
			)
		}
	}
}

func (rt *roundTripper) shouldBeRetried(err error) bool {
	return err != nil && rt.retriableClassifier.Classify(err)
}

type selectEndpointError struct {
	err error
}

func (s selectEndpointError) Error() string {
	return s.err.Error()
}

func (rt *roundTripper) doRoundTripToBackend(iter route.EndpointIterator, request *http.Request, reqInfo *handlers.RequestInfo, attempt int) (*route.Endpoint, *http.Response, logger.Logger, error) {
	println("======= doing the trip to backend")
	endpoint, err := rt.selectEndpoint(iter, request)
	if err != nil {
		println("============== fucked up, there are no endpoints")
		return endpoint, nil, rt.logger, &selectEndpointError{err: err}
	}
	logger := rt.logger.With(zap.Nest("route-endpoint", endpoint.ToLogData()...))
	reqInfo.RouteEndpoint = endpoint
	if endpoint.IsTLS() {
		request.URL.Scheme = "https"
	} else {
		request.URL.Scheme = "http"
	}

	logger.Debug("backend", zap.Int("attempt", attempt))
	res, err := rt.backendRoundTrip(request, endpoint, iter)
	if err != nil {
		iter.EndpointFailed(err)
	}

	return endpoint, res, logger, err
}

func (rt *roundTripper) CancelRequest(request *http.Request) {
	endpoint, err := handlers.GetEndpoint(request.Context())
	if err != nil {
		return
	}

	tr := GetRoundTripper(endpoint, rt.roundTripperFactory, false)
	tr.CancelRequest(request)
}

func (rt *roundTripper) backendRoundTrip(
	request *http.Request,
	endpoint *route.Endpoint,
	iter route.EndpointIterator,
) (*http.Response, error) {
	request.URL.Host = endpoint.CanonicalAddr()
	request.Header.Set("X-CF-ApplicationID", endpoint.ApplicationId)
	request.Header.Set("X-CF-InstanceIndex", endpoint.PrivateInstanceIndex)
	handler.SetRequestXCfInstanceId(request, endpoint)

	// increment connection stats
	iter.PreRequest(endpoint)

	rt.combinedReporter.CaptureRoutingRequest(endpoint)
	tr := GetRoundTripper(endpoint, rt.roundTripperFactory, false)
	res, err := rt.timedRoundTrip(tr, request)

	// decrement connection stats
	iter.PostRequest(endpoint)
	return res, err
}

func (rt *roundTripper) timedRoundTrip(tr http.RoundTripper, request *http.Request) (*http.Response, error) {
	if rt.endpointTimeout <= 0 {
		return tr.RoundTrip(request)
	}

	reqCtx, cancel := context.WithTimeout(request.Context(), rt.endpointTimeout)
	request = request.WithContext(reqCtx)

	// unfortunately if the cancel function above is not called that
	// results in a vet error
	go func() {
		select {
		case <-reqCtx.Done():
			cancel()
		}
	}()

	resp, err := tr.RoundTrip(request)
	if err != nil {
		cancel()
		return nil, err
	}

	return resp, nil
}

func (rt *roundTripper) selectEndpoint(iter route.EndpointIterator, request *http.Request) (*route.Endpoint, error) {
	endpoint := iter.Next()
	if endpoint == nil {
		return nil, handler.NoEndpointsAvailable
	}

	return endpoint, nil
}

func setupStickySession(
	response *http.Response,
	endpoint *route.Endpoint,
	originalEndpointId string,
	secureCookies bool,
	path string,
	stickySessionCookieNames config.StringSet,
) {
	secure := false
	maxAge := 0
	sameSite := http.SameSite(0)

	// did the endpoint change?
	sticky := originalEndpointId != "" && originalEndpointId != endpoint.PrivateInstanceId

	for _, v := range response.Cookies() {
		if _, ok := stickySessionCookieNames[v.Name]; ok {
			sticky = true
			if v.MaxAge < 0 {
				maxAge = v.MaxAge
			}
			secure = v.Secure
			sameSite = v.SameSite
			break
		}
	}

	for _, v := range response.Cookies() {
		if v.Name == VcapCookieId {
			sticky = false
			break
		}
	}

	if sticky {
		// right now secure attribute would as equal to the JSESSION ID cookie (if present),
		// but override if set to true in config
		if secureCookies {
			secure = true
		}

		cookie := &http.Cookie{
			Name:     VcapCookieId,
			Value:    endpoint.PrivateInstanceId,
			Path:     path,
			MaxAge:   maxAge,
			HttpOnly: true,
			Secure:   secure,
			SameSite: sameSite,
		}

		if v := cookie.String(); v != "" {
			response.Header.Add(CookieHeader, v)
		}
	}
}

func getStickySession(request *http.Request, stickySessionCookieNames config.StringSet) string {
	// Try choosing a backend using sticky session
	for stickyCookieName, _ := range stickySessionCookieNames {
		if _, err := request.Cookie(stickyCookieName); err == nil {
			if sticky, err := request.Cookie(VcapCookieId); err == nil {
				return sticky.Value
			}
		}
	}
	return ""
}
