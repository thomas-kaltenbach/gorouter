package registry_test

import (
	. "github.com/cloudfoundry/gorouter/registry"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/cloudfoundry/gorouter/config"
	"github.com/cloudfoundry/gorouter/route"
	"github.com/cloudfoundry/yagnats/fakeyagnats"

	"encoding/json"
	"time"
)

var _ = Describe("RouteRegistry", func() {
	var r *RouteRegistry
	var messageBus *fakeyagnats.FakeNATSConn

	var fooEndpoint, barEndpoint, bar2Endpoint *route.Endpoint
	var configObj *config.Config

	BeforeEach(func() {
		configObj = config.DefaultConfig()
		configObj.PruneStaleDropletsInterval = 50 * time.Millisecond
		configObj.DropletStaleThreshold = 10 * time.Millisecond

		messageBus = fakeyagnats.Connect()
		r = NewRouteRegistry(configObj, messageBus)
		fooEndpoint = route.NewEndpoint("12345", "192.168.1.1", 1234,
			"id1", map[string]string{
				"runtime":   "ruby18",
				"framework": "sinatra",
			}, -1, "")

		barEndpoint = route.NewEndpoint("54321", "192.168.1.2", 4321,
			"id2", map[string]string{
				"runtime":   "javascript",
				"framework": "node",
			}, -1, "https://my-rs.com")

		bar2Endpoint = route.NewEndpoint("54321", "192.168.1.3", 1234,
			"id3", map[string]string{
				"runtime":   "javascript",
				"framework": "node",
			}, -1, "")
	})

	Context("Register", func() {
		Context("uri", func() {
			It("records and tracks time of last update", func() {
				r.Register("foo", fooEndpoint)
				r.Register("fooo", fooEndpoint)
				Expect(r.NumUris()).To(Equal(2))
				firstUpdateTime := r.TimeOfLastUpdate()

				r.Register("bar", barEndpoint)
				r.Register("baar", barEndpoint)
				Expect(r.NumUris()).To(Equal(4))
				secondUpdateTime := r.TimeOfLastUpdate()

				Expect(secondUpdateTime.After(firstUpdateTime)).To(BeTrue())
			})

			It("ignores duplicates", func() {
				r.Register("bar", barEndpoint)
				r.Register("baar", barEndpoint)

				Expect(r.NumUris()).To(Equal(2))
				Expect(r.NumEndpoints()).To(Equal(1))

				r.Register("bar", barEndpoint)
				r.Register("baar", barEndpoint)

				Expect(r.NumUris()).To(Equal(2))
				Expect(r.NumEndpoints()).To(Equal(1))
			})

			It("ignores case", func() {
				m1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")
				m2 := route.NewEndpoint("", "192.168.1.1", 1235, "", nil, -1, "")

				r.Register("foo", m1)
				r.Register("FOO", m2)

				Expect(r.NumUris()).To(Equal(1))
			})

			It("allows multiple uris for the same endpoint", func() {
				m1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")
				m2 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")

				r.Register("foo", m1)
				r.Register("bar", m2)

				Expect(r.NumUris()).To(Equal(2))
				Expect(r.NumEndpoints()).To(Equal(1))
			})

			It("allows routes with paths", func() {
				m1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")

				r.Register("foo", m1)
				r.Register("foo/v1", m1)

				Expect(r.NumUris()).To(Equal(2))
				Expect(r.NumEndpoints()).To(Equal(1))

			})
		})

		Context("wildcard routes", func() {
			It("records a uri starting with a '*' ", func() {
				r.Register("*.a.route", fooEndpoint)

				Expect(r.NumUris()).To(Equal(1))
				Expect(r.NumEndpoints()).To(Equal(1))
			})
		})
	})

	Context("Unregister", func() {
		It("Handles unknown URIs", func() {
			r.Unregister("bar", barEndpoint)
			Expect(r.NumUris()).To(Equal(0))
			Expect(r.NumEndpoints()).To(Equal(0))
		})

		It("removes uris and endpoints", func() {
			r.Register("bar", barEndpoint)
			r.Register("baar", barEndpoint)
			Expect(r.NumUris()).To(Equal(2))
			Expect(r.NumEndpoints()).To(Equal(1))

			r.Register("bar", bar2Endpoint)
			r.Register("baar", bar2Endpoint)
			Expect(r.NumUris()).To(Equal(2))
			Expect(r.NumEndpoints()).To(Equal(2))

			r.Unregister("bar", barEndpoint)
			r.Unregister("baar", barEndpoint)
			Expect(r.NumUris()).To(Equal(2))
			Expect(r.NumEndpoints()).To(Equal(1))

			r.Unregister("bar", bar2Endpoint)
			r.Unregister("baar", bar2Endpoint)
			Expect(r.NumUris()).To(Equal(0))
			Expect(r.NumEndpoints()).To(Equal(0))
		})

		It("ignores uri case and matches endpoint", func() {
			m1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")
			m2 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")

			r.Register("foo", m1)
			r.Unregister("FOO", m2)

			Expect(r.NumUris()).To(Equal(0))
		})

		It("removes the specific url/endpoint combo", func() {
			m1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")
			m2 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")

			r.Register("foo", m1)
			r.Register("bar", m1)

			r.Unregister("foo", m2)

			Expect(r.NumUris()).To(Equal(1))
		})

		It("removes wildcard routes", func() {
			r.Register("*.bar", barEndpoint)
			r.Register("*.baar", barEndpoint)
			Expect(r.NumUris()).To(Equal(2))
			Expect(r.NumEndpoints()).To(Equal(1))

			r.Register("*.bar", bar2Endpoint)
			r.Register("*.baar", bar2Endpoint)
			Expect(r.NumUris()).To(Equal(2))
			Expect(r.NumEndpoints()).To(Equal(2))

			r.Unregister("*.bar", barEndpoint)
			r.Unregister("*.baar", barEndpoint)
			Expect(r.NumUris()).To(Equal(2))
			Expect(r.NumEndpoints()).To(Equal(1))

			r.Unregister("*.bar", bar2Endpoint)
			r.Unregister("*.baar", bar2Endpoint)
			Expect(r.NumUris()).To(Equal(0))
			Expect(r.NumEndpoints()).To(Equal(0))
		})

		It("removes a route with a path", func() {
			m1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")

			r.Register("foo/bar", m1)
			r.Unregister("foo/bar", m1)

			Expect(r.NumUris()).To(Equal(0))
		})

		It("only unregisters the exact uri", func() {
			m1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")

			r.Register("foo", m1)
			r.Register("foo/bar", m1)

			r.Unregister("foo", m1)
			Expect(r.NumUris()).To(Equal(1))

			p1 := r.Lookup("foo/bar")
			iter := p1.Endpoints("")
			Expect(iter.Next().CanonicalAddr()).To(Equal("192.168.1.1:1234"))

			p2 := r.Lookup("foo")
			Expect(p2).To(BeNil())
		})
	})

	Context("Lookup", func() {
		It("case insensitive lookup", func() {
			m := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")

			r.Register("foo", m)

			p1 := r.Lookup("foo")
			p2 := r.Lookup("FOO")
			Expect(p1).To(Equal(p2))

			iter := p1.Endpoints("")
			Expect(iter.Next().CanonicalAddr()).To(Equal("192.168.1.1:1234"))
		})

		It("selects one of the routes", func() {
			m1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")
			m2 := route.NewEndpoint("", "192.168.1.1", 1235, "", nil, -1, "")

			r.Register("bar", m1)
			r.Register("barr", m1)

			r.Register("bar", m2)
			r.Register("barr", m2)

			Expect(r.NumUris()).To(Equal(2))
			Expect(r.NumEndpoints()).To(Equal(2))

			p := r.Lookup("bar")
			Expect(p).ToNot(BeNil())
			e := p.Endpoints("").Next()
			Expect(e).ToNot(BeNil())
			Expect(e.CanonicalAddr()).To(MatchRegexp("192.168.1.1:123[4|5]"))
		})

		It("selects the outer most wild card route if one exists", func() {
			app1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")
			app2 := route.NewEndpoint("", "192.168.1.2", 1234, "", nil, -1, "")

			r.Register("*.outer.wild.card", app1)
			r.Register("*.wild.card", app2)

			p := r.Lookup("foo.wild.card")
			Expect(p).ToNot(BeNil())
			e := p.Endpoints("").Next()
			Expect(e).ToNot(BeNil())
			Expect(e.CanonicalAddr()).To(Equal("192.168.1.2:1234"))

			p = r.Lookup("foo.space.wild.card")
			Expect(p).ToNot(BeNil())
			e = p.Endpoints("").Next()
			Expect(e).ToNot(BeNil())
			Expect(e.CanonicalAddr()).To(Equal("192.168.1.2:1234"))
		})

		It("prefers full URIs to wildcard routes", func() {
			app1 := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")
			app2 := route.NewEndpoint("", "192.168.1.2", 1234, "", nil, -1, "")

			r.Register("not.wild.card", app1)
			r.Register("*.wild.card", app2)

			p := r.Lookup("not.wild.card")
			Expect(p).ToNot(BeNil())
			e := p.Endpoints("").Next()
			Expect(e).ToNot(BeNil())
			Expect(e.CanonicalAddr()).To(Equal("192.168.1.1:1234"))
		})
	})

	Context("Prunes Stale Droplets", func() {

		AfterEach(func() {
			r.StopPruningCycle()
		})

		It("removes stale droplets", func() {
			r.Register("foo", fooEndpoint)
			r.Register("fooo", fooEndpoint)

			r.Register("bar", barEndpoint)
			r.Register("baar", barEndpoint)

			Expect(r.NumUris()).To(Equal(4))
			Expect(r.NumEndpoints()).To(Equal(2))

			r.StartPruningCycle()
			time.Sleep(configObj.PruneStaleDropletsInterval + 10*time.Millisecond)

			Expect(r.NumUris()).To(Equal(0))
			Expect(r.NumEndpoints()).To(Equal(0))

			marshalled, err := json.Marshal(r)
			Ω(err).NotTo(HaveOccurred())
			Expect(string(marshalled)).To(Equal(`{}`))
		})

		It("skips fresh droplets", func() {
			endpoint := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "")

			r.Register("foo", endpoint)
			r.Register("bar", endpoint)

			r.Register("foo", endpoint)

			Expect(r.NumUris()).To(Equal(2))
			Expect(r.NumEndpoints()).To(Equal(1))

			r.StartPruningCycle()
			time.Sleep(configObj.PruneStaleDropletsInterval + 10*time.Millisecond)

			r.Register("foo", endpoint)

			r.StopPruningCycle()
			Expect(r.NumUris()).To(Equal(1))
			Expect(r.NumEndpoints()).To(Equal(1))

			p := r.Lookup("foo")
			Expect(p).ToNot(BeNil())
			Expect(p.Endpoints("").Next()).To(Equal(endpoint))

			p = r.Lookup("bar")
			Expect(p).To(BeNil())
		})

		It("does not block when pruning", func() {
			// when pruning stale droplets,
			// and the stale check takes a while,
			// and a read request comes in (i.e. from Lookup),
			// the read request completes before the stale check

			r.Register("foo", fooEndpoint)
			r.Register("fooo", fooEndpoint)

			r.StartPruningCycle()

			p := r.Lookup("foo")
			Expect(p).ToNot(BeNil())
		})
	})

	Context("Varz data", func() {
		It("NumUris", func() {
			r.Register("bar", barEndpoint)
			r.Register("baar", barEndpoint)

			Expect(r.NumUris()).To(Equal(2))

			r.Register("foo", fooEndpoint)

			Expect(r.NumUris()).To(Equal(3))
		})

		It("NumEndpoints", func() {
			r.Register("bar", barEndpoint)
			r.Register("baar", barEndpoint)

			Expect(r.NumEndpoints()).To(Equal(1))

			r.Register("foo", fooEndpoint)

			Expect(r.NumEndpoints()).To(Equal(2))
		})

		It("TimeOfLastUpdate", func() {
			start := time.Now()
			r.Register("bar", barEndpoint)
			t := r.TimeOfLastUpdate()
			end := time.Now()

			Expect(start.Before(t)).To(BeTrue())
			Expect(end.After(t)).To(BeTrue())
		})
	})

	It("marshals", func() {
		m := route.NewEndpoint("", "192.168.1.1", 1234, "", nil, -1, "https://my-routeService.com")
		r.Register("foo", m)

		marshalled, err := json.Marshal(r)
		Ω(err).NotTo(HaveOccurred())
		Expect(string(marshalled)).To(Equal(`{"foo":[{"address":"192.168.1.1:1234","ttl":-1,"route_service_url":"https://my-routeService.com"}]}`))
		r.Unregister("foo", m)
		marshalled, err = json.Marshal(r)
		Ω(err).NotTo(HaveOccurred())
		Expect(string(marshalled)).To(Equal(`{}`))
	})
})
