package portmap

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"lukechampine.com/upnp"
)

type testRouter struct {
	mu                sync.Mutex
	rule              upnp.PortMapping
	exists, permanent bool
	loseAddResponse   bool
	wan               string
	adds, deletes     int
	leases            []uint32
}

func (r *testRouter) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w.Header().Set("Content-Type", "text/xml")
	if req.Method == "GET" {
		fmt.Fprint(w, `<root xmlns="urn:schemas-upnp-org:device-1-0"><device><serviceList><service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>control</controlURL></service></serviceList></device></root>`)
		return
	}
	if req.URL.Path != "/control" {
		http.Error(w, "bad control path", 404)
		return
	}
	var envelope struct {
		Body struct {
			Action struct {
				External    uint16 `xml:"NewExternalPort"`
				Internal    uint16 `xml:"NewInternalPort"`
				Client      string `xml:"NewInternalClient"`
				Protocol    string `xml:"NewProtocol"`
				Description string `xml:"NewPortMappingDescription"`
				Lease       uint32 `xml:"NewLeaseDuration"`
			} `xml:",any"`
		} `xml:"Body"`
	}
	if xml.NewDecoder(req.Body).Decode(&envelope) != nil {
		http.Error(w, "invalid XML", 400)
		return
	}
	a := envelope.Body.Action
	action := strings.TrimSuffix(strings.Split(req.Header.Get("SOAPAction"), "#")[1], `"`)
	fault := func(code int) {
		w.WriteHeader(500)
		fmt.Fprintf(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode><errorDescription>Test router error</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`, code)
	}
	var body string
	if action != "GetExternalIPAddress" && (a.External != 41000 || a.Protocol != "TCP") {
		fault(402)
		return
	}
	switch action {
	case "GetSpecificPortMappingEntry":
		if !r.exists {
			fault(714)
			return
		}
		body = fmt.Sprintf("<NewInternalPort>%d</NewInternalPort><NewInternalClient>%s</NewInternalClient><NewEnabled>1</NewEnabled><NewPortMappingDescription>%s</NewPortMappingDescription>", r.rule.NewInternalPort, r.rule.NewInternalClient, r.rule.NewPortMappingDescription)
	case "AddPortMapping":
		r.leases = append(r.leases, a.Lease)
		if r.permanent && a.Lease != 0 {
			fault(725)
			return
		}
		if a.Internal != 41000 || a.Client != "127.0.0.1" {
			fault(402)
			return
		}
		r.rule = upnp.PortMapping{NewInternalPort: a.Internal, NewInternalClient: a.Client, NewEnabled: true, NewPortMappingDescription: a.Description}
		r.exists = true
		r.adds++
		if r.loseAddResponse {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
	case "DeletePortMapping":
		r.exists = false
		r.deletes++
	case "GetExternalIPAddress":
		body = "<NewExternalIPAddress>" + r.wan + "</NewExternalIPAddress>"
	default:
		fault(401)
		return
	}
	fmt.Fprintf(w, `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:%sResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">%s</u:%sResponse></s:Body></s:Envelope>`, action, body, action)
}

func connectedRouter(t *testing.T, r *testRouter) device {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(server.Close)
	d, err := upnp.Connect(context.Background(), server.URL+"/description.xml")
	if err != nil {
		t.Fatal(err)
	}
	return device{d}
}

func TestUPnPLifecycle(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(fmt.Sprint("permanent=", permanent), func(t *testing.T) {
			r := &testRouter{wan: "8.8.8.8", permanent: permanent}
			d := connectedRouter(t, r)
			ctx, cancel := context.WithCancel(context.Background())
			m := &Manager{status: Status{Port: 41000}, cancel: cancel, done: make(chan struct{})}
			go m.run(ctx, "QDAY P2P test-owner", func(context.Context) (router, error) { return d, nil }, 20*time.Millisecond, 20*time.Millisecond)
			defer m.Close()
			deadline := time.Now().Add(3 * time.Second)
			for {
				r.mu.Lock()
				renewed := r.adds >= 2
				r.mu.Unlock()
				if renewed && m.Status().State == "mapped" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("mapping failed: %+v", m.Status())
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !m.Status().PublicIP || m.Status().ExternalAddress != "8.8.8.8:41000" {
				t.Fatal("incorrect mapping status")
			}
			m.Close()
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.exists || r.deletes != 1 {
				t.Fatal("owned mapping was not removed")
			}
			if len(r.leases) == 0 || r.leases[0] != 3600 {
				t.Fatal("did not request a renewable lease")
			}
			if permanent && (len(r.leases) < 2 || r.leases[1] != 0) {
				t.Fatal("legacy permanent-lease fallback missing")
			}
		})
	}
}

func TestUPnPOwnershipAndUpstreamNAT(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "192.168.1.99"} {
		r := &testRouter{exists: true, wan: "100.64.1.2", rule: upnp.PortMapping{NewInternalClient: host, NewInternalPort: 41000, NewEnabled: true, NewPortMappingDescription: "Manual rule"}}
		d := connectedRouter(t, r)
		m := &Manager{status: Status{Port: 41000}}
		owned, err := m.ensure(context.Background(), d, "QDAY P2P test-owner")
		if owned {
			t.Fatal("claimed an existing manual mapping")
		}
		if host == "127.0.0.1" && (err != nil || m.Status().State != "upstream-nat" || m.Status().PublicIP) {
			t.Fatalf("CGNAT was reported as public: %+v %v", m.Status(), err)
		}
		if host != "127.0.0.1" && err == nil {
			t.Fatal("conflicting router destination accepted")
		}
		m.cleanup(d, "QDAY P2P test-owner")
		r.mu.Lock()
		modified := r.adds != 0 || r.deletes != 0 || !r.exists
		r.mu.Unlock()
		if modified {
			t.Fatal("another application's rule was changed")
		}
	}
	// Ownership may change between renewal and shutdown.
	r := &testRouter{wan: "8.8.8.8"}
	d := connectedRouter(t, r)
	m := &Manager{status: Status{Port: 41000}}
	if _, err := m.ensure(context.Background(), d, "QDAY P2P test-owner"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.rule.NewPortMappingDescription = "Replacement owner"
	r.mu.Unlock()
	m.cleanup(d, "QDAY P2P test-owner")
	r.mu.Lock()
	deleted := r.deletes
	r.mu.Unlock()
	if deleted != 0 {
		t.Fatal("shutdown deleted a replacement rule")
	}
}

func TestUPnPShutdownCancelsDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{status: Status{Port: 41000}, cancel: cancel, done: make(chan struct{})}
	entered := make(chan struct{})
	go m.run(ctx, "test-owner", func(ctx context.Context) (router, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }, time.Hour, time.Hour)
	<-entered
	start := time.Now()
	m.Close()
	if time.Since(start) > time.Second {
		t.Fatal("shutdown waited for discovery timeout")
	}
}

func TestUPnPLostResponseStillCleansUp(t *testing.T) {
	r := &testRouter{wan: "8.8.8.8", loseAddResponse: true}
	d := connectedRouter(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{status: Status{Port: 41000}, cancel: cancel, done: make(chan struct{})}
	go m.run(ctx, "QDAY P2P test-owner", func(context.Context) (router, error) { return d, nil }, time.Hour, time.Hour)
	defer m.Close()
	deadline := time.Now().Add(3 * time.Second)
	for m.Status().State != "unavailable" {
		if time.Now().After(deadline) {
			t.Fatal("missing failed-response status")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.Close()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.adds != 1 || r.deletes != 1 || r.exists {
		t.Fatal("mapping survived a lost creation response and clean shutdown")
	}
}

func TestUPnPRequestCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := upnp.Connect(ctx, server.URL); err == nil {
		t.Fatal("unresponsive router succeeded")
	}
	if time.Since(start) > time.Second {
		t.Fatal("router request ignored context deadline")
	}
}
