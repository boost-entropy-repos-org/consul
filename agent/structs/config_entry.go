package structs

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-msgpack/codec"
	"github.com/hashicorp/go-multierror"
	"github.com/mitchellh/hashstructure"
	"github.com/mitchellh/mapstructure"

	"github.com/hashicorp/consul/acl"
	"github.com/hashicorp/consul/agent/cache"
	"github.com/hashicorp/consul/lib"
	"github.com/hashicorp/consul/lib/decode"
)

const (
	ServiceDefaults    string = "service-defaults"
	ProxyDefaults      string = "proxy-defaults"
	ServiceRouter      string = "service-router"
	ServiceSplitter    string = "service-splitter"
	ServiceResolver    string = "service-resolver"
	IngressGateway     string = "ingress-gateway"
	TerminatingGateway string = "terminating-gateway"
	ServiceIntentions  string = "service-intentions"

	ProxyConfigGlobal string = "global"

	DefaultServiceProtocol = "tcp"
)

var AllConfigEntryKinds = []string{
	ServiceDefaults,
	ProxyDefaults,
	ServiceRouter,
	ServiceSplitter,
	ServiceResolver,
	IngressGateway,
	TerminatingGateway,
	ServiceIntentions,
}

// ConfigEntry is the interface for centralized configuration stored in Raft.
// Currently only service-defaults and proxy-defaults are supported.
type ConfigEntry interface {
	GetKind() string
	GetName() string

	// This is called in the RPC endpoint and can apply defaults or limits.
	Normalize() error
	Validate() error

	// CanRead and CanWrite return whether or not the given Authorizer
	// has permission to read or write to the config entry, respectively.
	CanRead(acl.Authorizer) bool
	CanWrite(acl.Authorizer) bool

	GetMeta() map[string]string
	GetEnterpriseMeta() *EnterpriseMeta
	GetRaftIndex() *RaftIndex
}

// UpdatableConfigEntry is the optional interface implemented by a ConfigEntry
// if it wants more control over how the update part of upsert works
// differently than a straight create. By default without this implementation
// all upsert operations are replacements.
type UpdatableConfigEntry interface {
	// UpdateOver is called from the state machine when an identically named
	// config entry already exists. This lets the config entry optionally
	// choose to use existing information from a config entry (such as
	// CreateTime) to slightly adjust how the update actually happens.
	UpdateOver(prev ConfigEntry) error
	ConfigEntry
}

// ServiceConfiguration is the top-level struct for the configuration of a service
// across the entire cluster.
type ServiceConfigEntry struct {
	Kind             string
	Name             string
	Protocol         string
	MeshGateway      MeshGatewayConfig `json:",omitempty" alias:"mesh_gateway"`
	Expose           ExposeConfig      `json:",omitempty"`
	TransparentProxy bool              `json:",omitempty" alias:"transparent_proxy"`

	ExternalSNI string `json:",omitempty" alias:"external_sni"`

	Connect *ConnectConfiguration `json:",omitempty"`

	Meta           map[string]string `json:",omitempty"`
	EnterpriseMeta `hcl:",squash" mapstructure:",squash"`
	RaftIndex
}

func (e *ServiceConfigEntry) Clone() *ServiceConfigEntry {
	e2 := *e
	e2.Expose = e.Expose.Clone()
	return &e2
}

func (e *ServiceConfigEntry) GetKind() string {
	return ServiceDefaults
}

func (e *ServiceConfigEntry) GetName() string {
	if e == nil {
		return ""
	}

	return e.Name
}

func (e *ServiceConfigEntry) GetMeta() map[string]string {
	if e == nil {
		return nil
	}
	return e.Meta
}

func (e *ServiceConfigEntry) Normalize() error {
	if e == nil {
		return fmt.Errorf("config entry is nil")
	}

	e.Kind = ServiceDefaults
	e.Protocol = strings.ToLower(e.Protocol)

	e.Connect.Normalize()
	e.EnterpriseMeta.Normalize()

	return nil
}

func (e *ServiceConfigEntry) Validate() error {
	validationErr := validateConfigEntryMeta(e.Meta)

	if e.Connect != nil {
		err := e.Connect.Validate()
		if err != nil {
			validationErr = multierror.Append(validationErr, err)
		}
	}

	return validationErr
}

func (e *ServiceConfigEntry) CanRead(authz acl.Authorizer) bool {
	var authzContext acl.AuthorizerContext
	e.FillAuthzContext(&authzContext)
	return authz.ServiceRead(e.Name, &authzContext) == acl.Allow
}

func (e *ServiceConfigEntry) CanWrite(authz acl.Authorizer) bool {
	var authzContext acl.AuthorizerContext
	e.FillAuthzContext(&authzContext)
	return authz.ServiceWrite(e.Name, &authzContext) == acl.Allow
}

func (e *ServiceConfigEntry) GetRaftIndex() *RaftIndex {
	if e == nil {
		return &RaftIndex{}
	}

	return &e.RaftIndex
}

func (e *ServiceConfigEntry) GetEnterpriseMeta() *EnterpriseMeta {
	if e == nil {
		return nil
	}

	return &e.EnterpriseMeta
}

type ConnectConfiguration struct {
	// UpstreamConfigs is a map of <namespace/>service to per-upstream configuration
	UpstreamConfigs map[string]*UpstreamConfig `json:",omitempty" alias:"upstream_configs"`

	// UpstreamDefaults contains default configuration for all upstreams of a given service
	UpstreamDefaults *UpstreamConfig `json:",omitempty" alias:"upstream_defaults"`
}

func (cfg *ConnectConfiguration) Normalize() {
	if cfg == nil {
		return
	}
	for _, v := range cfg.UpstreamConfigs {
		v.Normalize()
	}

	cfg.UpstreamDefaults.Normalize()
}

func (cfg ConnectConfiguration) Validate() error {
	var validationErr error

	for k, v := range cfg.UpstreamConfigs {
		if err := v.Validate(); err != nil {
			validationErr = multierror.Append(validationErr, fmt.Errorf("error in upstream config for %s: %v", k, err))
		}
	}

	if err := cfg.UpstreamDefaults.Validate(); err != nil {
		validationErr = multierror.Append(validationErr, fmt.Errorf("error in upstream defaults %v", err))
	}

	return validationErr
}

// ProxyConfigEntry is the top-level struct for global proxy configuration defaults.
type ProxyConfigEntry struct {
	Kind             string
	Name             string
	Config           map[string]interface{}
	MeshGateway      MeshGatewayConfig `json:",omitempty" alias:"mesh_gateway"`
	Expose           ExposeConfig      `json:",omitempty"`
	TransparentProxy bool              `json:",omitempty" alias:"transparent_proxy"`

	Meta           map[string]string `json:",omitempty"`
	EnterpriseMeta `hcl:",squash" mapstructure:",squash"`
	RaftIndex
}

func (e *ProxyConfigEntry) GetKind() string {
	return ProxyDefaults
}

func (e *ProxyConfigEntry) GetName() string {
	if e == nil {
		return ""
	}

	return e.Name
}

func (e *ProxyConfigEntry) GetMeta() map[string]string {
	if e == nil {
		return nil
	}
	return e.Meta
}

func (e *ProxyConfigEntry) Normalize() error {
	if e == nil {
		return fmt.Errorf("config entry is nil")
	}

	e.Kind = ProxyDefaults
	e.Name = ProxyConfigGlobal

	e.EnterpriseMeta.Normalize()

	return nil
}

func (e *ProxyConfigEntry) Validate() error {
	if e == nil {
		return fmt.Errorf("config entry is nil")
	}

	if e.Name != ProxyConfigGlobal {
		return fmt.Errorf("invalid name (%q), only %q is supported", e.Name, ProxyConfigGlobal)
	}

	if err := validateConfigEntryMeta(e.Meta); err != nil {
		return err
	}

	return e.validateEnterpriseMeta()
}

func (e *ProxyConfigEntry) CanRead(authz acl.Authorizer) bool {
	return true
}

func (e *ProxyConfigEntry) CanWrite(authz acl.Authorizer) bool {
	var authzContext acl.AuthorizerContext
	e.FillAuthzContext(&authzContext)
	return authz.OperatorWrite(&authzContext) == acl.Allow
}

func (e *ProxyConfigEntry) GetRaftIndex() *RaftIndex {
	if e == nil {
		return &RaftIndex{}
	}

	return &e.RaftIndex
}

func (e *ProxyConfigEntry) GetEnterpriseMeta() *EnterpriseMeta {
	if e == nil {
		return nil
	}

	return &e.EnterpriseMeta
}

func (e *ProxyConfigEntry) MarshalBinary() (data []byte, err error) {
	// We mainly want to implement the BinaryMarshaller interface so that
	// we can fixup some msgpack types to coerce them into JSON compatible
	// values. No special encoding needs to be done - we just simply msgpack
	// encode the struct which requires a type alias to prevent recursively
	// calling this function.

	type alias ProxyConfigEntry

	a := alias(*e)

	// bs will grow if needed but allocate enough to avoid reallocation in common
	// case.
	bs := make([]byte, 128)
	enc := codec.NewEncoderBytes(&bs, MsgpackHandle)
	err = enc.Encode(a)
	if err != nil {
		return nil, err
	}

	return bs, nil
}

func (e *ProxyConfigEntry) UnmarshalBinary(data []byte) error {
	// The goal here is to add a post-decoding operation to
	// decoding of a ProxyConfigEntry. The cleanest way I could
	// find to do so was to implement the BinaryMarshaller interface
	// and use a type alias to do the original round of decoding,
	// followed by a MapWalk of the Config to coerce everything
	// into JSON compatible types.
	type alias ProxyConfigEntry

	var a alias
	dec := codec.NewDecoderBytes(data, MsgpackHandle)
	if err := dec.Decode(&a); err != nil {
		return err
	}

	*e = ProxyConfigEntry(a)

	config, err := lib.MapWalk(e.Config)
	if err != nil {
		return err
	}

	e.Config = config
	return nil
}

// DecodeConfigEntry can be used to decode a ConfigEntry from a raw map value.
// Currently its used in the HTTP API to decode ConfigEntry structs coming from
// JSON. Unlike some of our custom binary encodings we don't have a preamble including
// the kind so we will not have a concrete type to decode into. In those cases we must
// first decode into a map[string]interface{} and then call this function to decode
// into a concrete type.
//
// There is an 'api' variation of this in
// command/config/write/config_write.go:newDecodeConfigEntry
func DecodeConfigEntry(raw map[string]interface{}) (ConfigEntry, error) {
	var entry ConfigEntry

	kindVal, ok := raw["Kind"]
	if !ok {
		kindVal, ok = raw["kind"]
	}
	if !ok {
		return nil, fmt.Errorf("Payload does not contain a kind/Kind key at the top level")
	}

	if kindStr, ok := kindVal.(string); ok {
		newEntry, err := MakeConfigEntry(kindStr, "")
		if err != nil {
			return nil, err
		}
		entry = newEntry
	} else {
		return nil, fmt.Errorf("Kind value in payload is not a string")
	}

	var md mapstructure.Metadata
	decodeConf := &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			decode.HookWeakDecodeFromSlice,
			decode.HookTranslateKeys,
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToTimeHookFunc(time.RFC3339),
		),
		Metadata:         &md,
		Result:           &entry,
		WeaklyTypedInput: true,
	}

	decoder, err := mapstructure.NewDecoder(decodeConf)
	if err != nil {
		return nil, err
	}

	if err := decoder.Decode(raw); err != nil {
		return nil, err
	}

	if err := validateUnusedKeys(md.Unused); err != nil {
		return nil, err
	}
	return entry, nil
}

type ConfigEntryOp string

const (
	ConfigEntryUpsert    ConfigEntryOp = "upsert"
	ConfigEntryUpsertCAS ConfigEntryOp = "upsert-cas"
	ConfigEntryDelete    ConfigEntryOp = "delete"
)

// ConfigEntryRequest is used when creating/updating/deleting a ConfigEntry.
type ConfigEntryRequest struct {
	Op         ConfigEntryOp
	Datacenter string
	Entry      ConfigEntry

	WriteRequest
}

func (c *ConfigEntryRequest) RequestDatacenter() string {
	return c.Datacenter
}

func (c *ConfigEntryRequest) MarshalBinary() (data []byte, err error) {
	// bs will grow if needed but allocate enough to avoid reallocation in common
	// case.
	bs := make([]byte, 128)
	enc := codec.NewEncoderBytes(&bs, MsgpackHandle)
	// Encode kind first
	err = enc.Encode(c.Entry.GetKind())
	if err != nil {
		return nil, err
	}
	// Then actual value using alias trick to avoid infinite recursion
	type Alias ConfigEntryRequest
	err = enc.Encode(struct {
		*Alias
	}{
		Alias: (*Alias)(c),
	})
	if err != nil {
		return nil, err
	}
	return bs, nil
}

func (c *ConfigEntryRequest) UnmarshalBinary(data []byte) error {
	// First decode the kind prefix
	var kind string
	dec := codec.NewDecoderBytes(data, MsgpackHandle)
	if err := dec.Decode(&kind); err != nil {
		return err
	}

	// Then decode the real thing with appropriate kind of ConfigEntry
	entry, err := MakeConfigEntry(kind, "")
	if err != nil {
		return err
	}
	c.Entry = entry

	// Alias juggling to prevent infinite recursive calls back to this decode
	// method.
	type Alias ConfigEntryRequest
	as := struct {
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	if err := dec.Decode(&as); err != nil {
		return err
	}
	return nil
}

func MakeConfigEntry(kind, name string) (ConfigEntry, error) {
	switch kind {
	case ServiceDefaults:
		return &ServiceConfigEntry{Name: name}, nil
	case ProxyDefaults:
		return &ProxyConfigEntry{Name: name}, nil
	case ServiceRouter:
		return &ServiceRouterConfigEntry{Name: name}, nil
	case ServiceSplitter:
		return &ServiceSplitterConfigEntry{Name: name}, nil
	case ServiceResolver:
		return &ServiceResolverConfigEntry{Name: name}, nil
	case IngressGateway:
		return &IngressGatewayConfigEntry{Name: name}, nil
	case TerminatingGateway:
		return &TerminatingGatewayConfigEntry{Name: name}, nil
	case ServiceIntentions:
		return &ServiceIntentionsConfigEntry{Name: name}, nil
	default:
		return nil, fmt.Errorf("invalid config entry kind: %s", kind)
	}
}

func ValidateConfigEntryKind(kind string) bool {
	switch kind {
	case ServiceDefaults, ProxyDefaults:
		return true
	case ServiceRouter, ServiceSplitter, ServiceResolver:
		return true
	case IngressGateway, TerminatingGateway:
		return true
	case ServiceIntentions:
		return true
	default:
		return false
	}
}

// ConfigEntryQuery is used when requesting info about a config entry.
type ConfigEntryQuery struct {
	Kind       string
	Name       string
	Datacenter string

	EnterpriseMeta `hcl:",squash" mapstructure:",squash"`
	QueryOptions
}

func (c *ConfigEntryQuery) RequestDatacenter() string {
	return c.Datacenter
}

func (r *ConfigEntryQuery) CacheInfo() cache.RequestInfo {
	info := cache.RequestInfo{
		Token:          r.Token,
		Datacenter:     r.Datacenter,
		MinIndex:       r.MinQueryIndex,
		Timeout:        r.MaxQueryTime,
		MaxAge:         r.MaxAge,
		MustRevalidate: r.MustRevalidate,
	}

	v, err := hashstructure.Hash([]interface{}{
		r.Kind,
		r.Name,
		r.Filter,
		r.EnterpriseMeta,
	}, nil)
	if err == nil {
		// If there is an error, we don't set the key. A blank key forces
		// no cache for this request so the request is forwarded directly
		// to the server.
		info.Key = strconv.FormatUint(v, 10)
	}

	return info
}

// ConfigEntryListAllRequest is used when requesting to list all config entries
// of a set of kinds.
type ConfigEntryListAllRequest struct {
	// Kinds should always be set. For backwards compatibility with versions
	// prior to 1.9.0, if this is omitted or left empty it is assumed to mean
	// the subset of config entry kinds that were present in 1.8.0:
	//
	// proxy-defaults, service-defaults, service-resolver, service-splitter,
	// service-router, terminating-gateway, and ingress-gateway.
	Kinds      []string
	Datacenter string

	EnterpriseMeta `hcl:",squash" mapstructure:",squash"`
	QueryOptions
}

func (r *ConfigEntryListAllRequest) RequestDatacenter() string {
	return r.Datacenter
}

// ServiceConfigRequest is used when requesting the resolved configuration
// for a service.
type ServiceConfigRequest struct {
	Name       string
	Datacenter string

	// MeshGateway contains the mesh gateway configuration from the requesting proxy's registration
	MeshGateway MeshGatewayConfig

	UpstreamIDs []ServiceID

	// DEPRECATED
	// Upstreams is a list of upstream service names to use for resolving the service config
	// UpstreamIDs should be used instead which can encode more than just the name to
	// uniquely identify a service.
	Upstreams []string

	EnterpriseMeta `hcl:",squash" mapstructure:",squash"`
	QueryOptions
}

func (s *ServiceConfigRequest) RequestDatacenter() string {
	return s.Datacenter
}

func (r *ServiceConfigRequest) CacheInfo() cache.RequestInfo {
	info := cache.RequestInfo{
		Token:          r.Token,
		Datacenter:     r.Datacenter,
		MinIndex:       r.MinQueryIndex,
		Timeout:        r.MaxQueryTime,
		MaxAge:         r.MaxAge,
		MustRevalidate: r.MustRevalidate,
	}

	// To calculate the cache key we only hash the service name and upstream set.
	// We don't want ordering of the upstreams to affect the outcome so use an
	// anonymous struct field with hash:set behavior. Note the order of fields in
	// the slice would affect cache keys if we ever persist between agent restarts
	// and change it.
	v, err := hashstructure.Hash(struct {
		Name           string
		EnterpriseMeta EnterpriseMeta
		Upstreams      []string `hash:"set"`
	}{
		Name:           r.Name,
		EnterpriseMeta: r.EnterpriseMeta,
		Upstreams:      r.Upstreams,
	}, nil)
	if err == nil {
		// If there is an error, we don't set the key. A blank key forces
		// no cache for this request so the request is forwarded directly
		// to the server.
		info.Key = strconv.FormatUint(v, 10)
	}

	return info
}

type UpstreamConfig struct {
	// EnvoyListenerJSON is a complete override ("escape hatch") for the upstream's
	// listener.
	//
	// Note: This escape hatch is NOT compatible with the discovery chain and
	// will be ignored if a discovery chain is active.
	EnvoyListenerJSON string `json:",omitempty" alias:"envoy_listener_json"`

	// EnvoyClusterJSON is a complete override ("escape hatch") for the upstream's
	// cluster. The Connect client TLS certificate and context will be injected
	// overriding any TLS settings present.
	//
	// Note: This escape hatch is NOT compatible with the discovery chain and
	// will be ignored if a discovery chain is active.
	EnvoyClusterJSON string `json:",omitempty" alias:"envoy_cluster_json"`

	// Protocol describes the upstream's service protocol. Valid values are "tcp",
	// "http" and "grpc". Anything else is treated as tcp. The enables protocol
	// aware features like per-request metrics and connection pooling, tracing,
	// routing etc.
	Protocol string `json:",omitempty"`

	// ConnectTimeoutMs is the number of milliseconds to timeout making a new
	// connection to this upstream. Defaults to 5000 (5 seconds) if not set.
	ConnectTimeoutMs int `json:",omitempty" alias:"connect_timeout_ms"`

	// Limits are the set of limits that are applied to the proxy for a specific upstream of a
	// service instance.
	Limits *UpstreamLimits `json:",omitempty"`

	// PassiveHealthCheck configuration determines how upstream proxy instances will
	// be monitored for removal from the load balancing pool.
	PassiveHealthCheck *PassiveHealthCheck `json:",omitempty" alias:"passive_health_check"`

	// MeshGatewayConfig controls how Mesh Gateways are configured and used
	MeshGateway MeshGatewayConfig `json:",omitempty" alias:"mesh_gateway" `
}

func (cfg UpstreamConfig) MergeInto(dst map[string]interface{}) {
	// Avoid storing empty values in the map, since these can act as overrides
	if cfg.EnvoyListenerJSON != "" {
		dst["envoy_listener_json"] = cfg.EnvoyListenerJSON
	}
	if cfg.EnvoyClusterJSON != "" {
		dst["envoy_cluster_json"] = cfg.EnvoyClusterJSON
	}
	if cfg.Protocol != "" {
		dst["protocol"] = cfg.Protocol
	}
	if cfg.ConnectTimeoutMs != 0 {
		dst["connect_timeout_ms"] = cfg.ConnectTimeoutMs
	}
	if !cfg.MeshGateway.IsZero() {
		dst["mesh_gateway"] = cfg.MeshGateway
	}
	if cfg.Limits != nil {
		dst["limits"] = cfg.Limits
	}
	if cfg.PassiveHealthCheck != nil {
		dst["passive_health_check"] = cfg.PassiveHealthCheck
	}
}

func (cfg *UpstreamConfig) Normalize() {
	cfg.Protocol = strings.ToLower(cfg.Protocol)

	if cfg.ConnectTimeoutMs < 0 {
		cfg.ConnectTimeoutMs = 0
	}
}

func (cfg UpstreamConfig) Validate() error {
	var validationErr error

	if cfg.PassiveHealthCheck != nil {
		err := cfg.PassiveHealthCheck.Validate()
		if err != nil {
			validationErr = multierror.Append(validationErr, err)
		}
	}

	if cfg.Limits != nil {
		err := cfg.Limits.Validate()
		if err != nil {
			validationErr = multierror.Append(validationErr, err)
		}
	}

	return validationErr
}

func ParseUpstreamConfigNoDefaults(m map[string]interface{}) (UpstreamConfig, error) {
	var cfg UpstreamConfig
	config := &mapstructure.DecoderConfig{
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			decode.HookWeakDecodeFromSlice,
			decode.HookTranslateKeys,
			mapstructure.StringToTimeDurationHookFunc(),
		),
		Result:           &cfg,
		WeaklyTypedInput: true,
	}

	decoder, err := mapstructure.NewDecoder(config)
	if err != nil {
		return cfg, err
	}

	err = decoder.Decode(m)
	cfg.Normalize()

	return cfg, err
}

// ParseUpstreamConfig returns the UpstreamConfig parsed from an opaque map.
// If an error occurs during parsing it is returned along with the default
// config this allows caller to choose whether and how to report the error.
func ParseUpstreamConfig(m map[string]interface{}) (UpstreamConfig, error) {
	cfg, err := ParseUpstreamConfigNoDefaults(m)

	// Set default (even if error is returned)
	if cfg.Protocol == "" {
		cfg.Protocol = "tcp"
	}
	if cfg.ConnectTimeoutMs == 0 {
		cfg.ConnectTimeoutMs = 5000
	}

	return cfg, err
}

type PassiveHealthCheck struct {
	// Interval between health check analysis sweeps. Each sweep may remove
	// hosts or return hosts to the pool.
	Interval time.Duration `json:",omitempty"`

	// MaxFailures is the count of consecutive failures that results in a host
	// being removed from the pool.
	MaxFailures uint32 `json:",omitempty" alias:"max_failures"`
}

func (chk *PassiveHealthCheck) IsZero() bool {
	zeroVal := PassiveHealthCheck{}
	return *chk == zeroVal
}

func (chk PassiveHealthCheck) Validate() error {
	if chk.Interval <= 0*time.Second {
		return fmt.Errorf("passive health check interval must be greater than 0s")
	}
	return nil
}

// UpstreamLimits describes the limits that are associated with a specific
// upstream of a service instance.
type UpstreamLimits struct {
	// MaxConnections is the maximum number of connections the local proxy can
	// make to the upstream service.
	MaxConnections *int `json:",omitempty" alias:"max_connections"`

	// MaxPendingRequests is the maximum number of requests that will be queued
	// waiting for an available connection. This is mostly applicable to HTTP/1.1
	// clusters since all HTTP/2 requests are streamed over a single
	// connection.
	MaxPendingRequests *int `json:",omitempty" alias:"max_pending_requests"`

	// MaxConcurrentRequests is the maximum number of in-flight requests that will be allowed
	// to the upstream cluster at a point in time. This is mostly applicable to HTTP/2
	// clusters since all HTTP/1.1 requests are limited by MaxConnections.
	MaxConcurrentRequests *int `json:",omitempty" alias:"max_concurrent_requests"`
}

func (ul *UpstreamLimits) IsZero() bool {
	zeroVal := UpstreamLimits{}
	return *ul == zeroVal
}

func (ul UpstreamLimits) Validate() error {
	if ul.MaxConnections != nil && *ul.MaxConnections <= 0 {
		return fmt.Errorf("max connections must be at least 0")
	}
	if ul.MaxPendingRequests != nil && *ul.MaxPendingRequests <= 0 {
		return fmt.Errorf("max pending requests must be at least 0")
	}
	if ul.MaxConcurrentRequests != nil && *ul.MaxConcurrentRequests <= 0 {
		return fmt.Errorf("max concurrent requests must be at least 0")
	}
	return nil
}

type OpaqueUpstreamConfig struct {
	Upstream ServiceID
	Config   map[string]interface{}
}

type OpaqueUpstreamConfigs []OpaqueUpstreamConfig

func (configs OpaqueUpstreamConfigs) GetUpstreamConfig(sid ServiceID) (config map[string]interface{}, found bool) {
	for _, usconf := range configs {
		if usconf.Upstream.Matches(sid) {
			return usconf.Config, true
		}
	}

	return nil, false
}

type ServiceConfigResponse struct {
	ProxyConfig       map[string]interface{}
	UpstreamConfigs   map[string]map[string]interface{}
	UpstreamIDConfigs OpaqueUpstreamConfigs
	MeshGateway       MeshGatewayConfig `json:",omitempty"`
	Expose            ExposeConfig      `json:",omitempty"`
	TransparentProxy  bool              `json:",omitempty"`
	QueryMeta
}

func (r *ServiceConfigResponse) Reset() {
	r.ProxyConfig = nil
	r.UpstreamConfigs = nil
	r.MeshGateway = MeshGatewayConfig{}
}

// MarshalBinary writes ServiceConfigResponse as msgpack encoded. It's only here
// because we need custom decoding of the raw interface{} values.
func (r *ServiceConfigResponse) MarshalBinary() (data []byte, err error) {
	// bs will grow if needed but allocate enough to avoid reallocation in common
	// case.
	bs := make([]byte, 128)
	enc := codec.NewEncoderBytes(&bs, MsgpackHandle)

	type Alias ServiceConfigResponse

	if err := enc.Encode((*Alias)(r)); err != nil {
		return nil, err
	}

	return bs, nil
}

// UnmarshalBinary decodes msgpack encoded ServiceConfigResponse. It used
// default msgpack encoding but fixes up the uint8 strings and other problems we
// have with encoding map[string]interface{}.
func (r *ServiceConfigResponse) UnmarshalBinary(data []byte) error {
	dec := codec.NewDecoderBytes(data, MsgpackHandle)

	type Alias ServiceConfigResponse
	var a Alias

	if err := dec.Decode(&a); err != nil {
		return err
	}

	*r = ServiceConfigResponse(a)

	var err error

	// Fix strings and maps in the returned maps
	r.ProxyConfig, err = lib.MapWalk(r.ProxyConfig)
	if err != nil {
		return err
	}
	for k := range r.UpstreamConfigs {
		r.UpstreamConfigs[k], err = lib.MapWalk(r.UpstreamConfigs[k])
		if err != nil {
			return err
		}
	}

	for k := range r.UpstreamIDConfigs {
		r.UpstreamIDConfigs[k].Config, err = lib.MapWalk(r.UpstreamIDConfigs[k].Config)
		if err != nil {
			return err
		}
	}
	return nil
}

// ConfigEntryResponse returns a single ConfigEntry
type ConfigEntryResponse struct {
	Entry ConfigEntry
	QueryMeta
}

func (c *ConfigEntryResponse) MarshalBinary() (data []byte, err error) {
	// bs will grow if needed but allocate enough to avoid reallocation in common
	// case.
	bs := make([]byte, 128)
	enc := codec.NewEncoderBytes(&bs, MsgpackHandle)

	if c.Entry != nil {
		if err := enc.Encode(c.Entry.GetKind()); err != nil {
			return nil, err
		}
		if err := enc.Encode(c.Entry); err != nil {
			return nil, err
		}
	} else {
		if err := enc.Encode(""); err != nil {
			return nil, err
		}
	}

	if err := enc.Encode(c.QueryMeta); err != nil {
		return nil, err
	}

	return bs, nil
}

func (c *ConfigEntryResponse) UnmarshalBinary(data []byte) error {
	dec := codec.NewDecoderBytes(data, MsgpackHandle)

	var kind string
	if err := dec.Decode(&kind); err != nil {
		return err
	}

	if kind != "" {
		entry, err := MakeConfigEntry(kind, "")
		if err != nil {
			return err
		}

		if err := dec.Decode(entry); err != nil {
			return err
		}
		c.Entry = entry
	} else {
		c.Entry = nil
	}

	if err := dec.Decode(&c.QueryMeta); err != nil {
		return err
	}

	return nil
}

func validateConfigEntryMeta(meta map[string]string) error {
	var err error
	if len(meta) > metaMaxKeyPairs {
		err = multierror.Append(err, fmt.Errorf(
			"Meta exceeds maximum element count %d", metaMaxKeyPairs))
	}
	for k, v := range meta {
		if len(k) > metaKeyMaxLength {
			err = multierror.Append(err, fmt.Errorf(
				"Meta key %q exceeds maximum length %d", k, metaKeyMaxLength))
		}
		if len(v) > metaValueMaxLength {
			err = multierror.Append(err, fmt.Errorf(
				"Meta value for key %q exceeds maximum length %d", k, metaValueMaxLength))
		}
	}
	return err
}
