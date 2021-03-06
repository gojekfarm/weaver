package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gojektech/weaver/pkg/shard"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gojektech/weaver"
	"github.com/gojektech/weaver/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ProxySuite struct {
	suite.Suite

	rtr *Router
}

func (ps *ProxySuite) SetupTest() {
	logger.SetupLogger()

	routeLoader := &mockRouteLoader{}

	ps.rtr = NewRouter(routeLoader)
	require.NotNil(ps.T(), ps.rtr)
}

func TestProxySuite(t *testing.T) {
	suite.Run(t, new(ProxySuite))
}

func (ps *ProxySuite) TestProxyHandlerOnSuccessfulRouting() {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("foobar"))
	}))

	acl := &weaver.ACL{
		ID:        "svc-01",
		Criterion: "Method(`GET`) && PathRegexp(`/(GF-|R-).*`)",
		EndpointConfig: &weaver.EndpointConfig{
			Matcher:   "path",
			ShardExpr: "/(GF-|R-|).*",
			ShardFunc: "lookup",
			ShardConfig: json.RawMessage(fmt.Sprintf(`{
				"GF-": {
					"backend_name": "foo",
					"backend":      "%s"
				},
				"R-": {
					"backend_name": "bar",
					"timeout":      100.0,
					"backend":      "http://iamgone"
				}
			}`, server.URL)),
		},
	}

	sharder, err := shard.New(acl.EndpointConfig.ShardFunc, acl.EndpointConfig.ShardConfig)
	require.NoError(ps.T(), err, "should not have failed to init a sharder")

	acl.Endpoint, err = weaver.NewEndpoint(acl.EndpointConfig, sharder)
	require.NoError(ps.T(), err, "should not have failed to set endpoint")

	_ = ps.rtr.UpsertRoute(acl.Criterion, acl)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/GF-1234", nil)

	proxy := proxy{router: ps.rtr}
	proxy.ServeHTTP(w, r)

	assert.Equal(ps.T(), http.StatusForbidden, w.Code)
	assert.Equal(ps.T(), "foobar", w.Body.String())
}

func (ps *ProxySuite) TestProxyHandlerOnBodyBasedMatcherWithModuloSharding() {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("foobar"))
	}))

	acl := &weaver.ACL{
		ID:        "svc-01",
		Criterion: "Method(`GET`) && PathRegexp(`/drivers`)",
		EndpointConfig: &weaver.EndpointConfig{
			Matcher:   "body",
			ShardExpr: ".drivers.id",
			ShardFunc: "modulo",
			ShardConfig: json.RawMessage(fmt.Sprintf(`{
				"0": {
					"backend_name": "foo",
					"backend":      "%s"
				},
				"1": {
					"backend_name": "bar",
					"timeout":      100.0,
					"backend":      "http://shard01"
				}
			}`, server.URL)),
		},
	}

	sharder, err := shard.New(acl.EndpointConfig.ShardFunc, acl.EndpointConfig.ShardConfig)
	require.NoError(ps.T(), err, "should not have failed to init a sharder")

	acl.Endpoint, err = weaver.NewEndpoint(acl.EndpointConfig, sharder)
	require.NoError(ps.T(), err, "should not have failed to set endpoint")

	_ = ps.rtr.UpsertRoute(acl.Criterion, acl)

	w := httptest.NewRecorder()
	body := bytes.NewReader([]byte(`{ "drivers": { "id": "122" } }`))
	r := httptest.NewRequest("GET", "/drivers", body)

	proxy := proxy{router: ps.rtr}
	proxy.ServeHTTP(w, r)

	assert.Equal(ps.T(), http.StatusOK, w.Code)
	assert.Equal(ps.T(), "foobar", w.Body.String())
}

func (ps *ProxySuite) TestProxyHandlerOnPathBasedMatcherWithModuloSharding() {

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("foobar"))
	}))

	acl := &weaver.ACL{
		ID:        "svc-01",
		Criterion: "Method(`GET`) && PathRegexp(`/drivers`)",
		EndpointConfig: &weaver.EndpointConfig{
			Matcher:   "path",
			ShardExpr: `/drivers/(\d+)`,
			ShardFunc: "modulo",
			ShardConfig: json.RawMessage(fmt.Sprintf(`{
				"0": {
					"backend_name": "foo",
					"backend":      "http://shard01"
				},
				"1": {
					"backend_name": "bar",
					"timeout":100.0,
					"backend":"%s"
				}
			}`, server.URL)),
		},
	}

	sharder, err := shard.New(acl.EndpointConfig.ShardFunc, acl.EndpointConfig.ShardConfig)
	require.NoError(ps.T(), err, "should not have failed to init a sharder")

	acl.Endpoint, err = weaver.NewEndpoint(acl.EndpointConfig, sharder)
	require.NoError(ps.T(), err, "should not have failed to set endpoint")

	_ = ps.rtr.UpsertRoute(acl.Criterion, acl)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/drivers/123", nil)

	proxy := proxy{router: ps.rtr}
	proxy.ServeHTTP(w, r)

	assert.Equal(ps.T(), http.StatusOK, w.Code)
	assert.Equal(ps.T(), "foobar", w.Body.String())
}

func (ps *ProxySuite) TestProxyHandlerOnFailureRouting() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/GF-1234", nil)

	proxy := proxy{router: ps.rtr}
	proxy.ServeHTTP(w, r)

	assert.Equal(ps.T(), http.StatusNotFound, w.Code)
	assert.Equal(ps.T(), "{\"errors\":[{\"code\":\"weaver:route:not_found\",\"message\":\"Something went wrong\",\"message_title\":\"Failure\",\"message_severity\":\"failure\"}]}", w.Body.String())
}

func (ps *ProxySuite) TestProxyHandlerOnMissingBackend() {

	acl := &weaver.ACL{
		ID:        "svc-01",
		Criterion: "Method(`GET`) && PathRegexp(`/(GF-|R-).*`)",
		EndpointConfig: &weaver.EndpointConfig{
			Matcher:   "path",
			ShardExpr: "/(GF-|R-|).*",
			ShardFunc: "lookup",
			ShardConfig: json.RawMessage(`{
				"R-": {
					"backend_name": "foo",
					"timeout":      100.0,
					"backend":      "http://iamgone"
				}
			}`),
		},
	}

	sharder, err := shard.New(acl.EndpointConfig.ShardFunc, acl.EndpointConfig.ShardConfig)
	require.NoError(ps.T(), err, "should not have failed to init a sharder")

	acl.Endpoint, err = weaver.NewEndpoint(acl.EndpointConfig, sharder)
	require.NoError(ps.T(), err, "should not have failed to set endpoint")

	_ = ps.rtr.UpsertRoute(acl.Criterion, acl)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/GF-1234", nil)

	proxy := proxy{router: ps.rtr}
	proxy.ServeHTTP(w, r)

	assert.Equal(ps.T(), http.StatusServiceUnavailable, w.Code)
}

func (ps *ProxySuite) TestHealthCheckWithPingRoute() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/ping", nil)

	proxy := proxy{router: ps.rtr}
	proxy.ServeHTTP(w, r)

	assert.Equal(ps.T(), http.StatusOK, w.Code)
}

func (ps *ProxySuite) TestHealthCheckWithDefaultRoute() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)

	proxy := proxy{router: ps.rtr}
	proxy.ServeHTTP(w, r)

	assert.Equal(ps.T(), http.StatusOK, w.Code)
}
