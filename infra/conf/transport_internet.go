package conf

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/platform/filesystem"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/finalmask/fragment"
	"github.com/xtls/xray-core/transport/internet/finalmask/header/custom"
	"github.com/xtls/xray-core/transport/internet/headers/noop"
		"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/tcp"
	"github.com/xtls/xray-core/transport/internet/tls"
	"google.golang.org/protobuf/proto"
)

var (
	tcpHeaderLoader = NewJSONConfigLoader(ConfigCreatorCache{
		"none": func() interface{} { return new(NoOpConnectionAuthenticator) },
	}, "type", "")
)

type TCPConfig struct {
	HeaderConfig        json.RawMessage `json:"header"`
	AcceptProxyProtocol bool            `json:"acceptProxyProtocol"`
}

// Build implements Buildable.
func (c *TCPConfig) Build() (proto.Message, error) {
	config := new(tcp.Config)
	if len(c.HeaderConfig) > 0 {
		headerConfig, _, err := tcpHeaderLoader.Load(c.HeaderConfig)
		if err != nil {
			return nil, errors.New("invalid TCP header config").Base(err).AtError()
		}
		ts, err := headerConfig.(Buildable).Build()
		if err != nil {
			return nil, errors.New("invalid TCP header config").Base(err).AtError()
		}
		config.HeaderSettings = serial.ToTypedMessage(ts)
	}
	if c.AcceptProxyProtocol {
		config.AcceptProxyProtocol = c.AcceptProxyProtocol
	}
	return config, nil
}


const (
	Byte     = 1
	Kilobyte = 1024 * Byte
	Megabyte = 1024 * Kilobyte
	Gigabyte = 1024 * Megabyte
	Terabyte = 1024 * Gigabyte
)


func readFileOrString(f string, s []string) ([]byte, error) {
	if len(f) > 0 {
		return filesystem.ReadCert(f)
	}
	if len(s) > 0 {
		return []byte(strings.Join(s, "\n")), nil
	}
	return nil, errors.New("both file and bytes are empty.")
}

type TLSCertConfig struct {
	CertFile       string   `json:"certificateFile"`
	CertStr        []string `json:"certificate"`
	KeyFile        string   `json:"keyFile"`
	KeyStr         []string `json:"key"`
	Usage          string   `json:"usage"`
	OcspStapling   uint64   `json:"ocspStapling"`
	OneTimeLoading bool     `json:"oneTimeLoading"`
	BuildChain     bool     `json:"buildChain"`
}

// Build implements Buildable.
func (c *TLSCertConfig) Build() (*tls.Certificate, error) {
	certificate := new(tls.Certificate)

	cert, err := readFileOrString(c.CertFile, c.CertStr)
	if err != nil {
		return nil, errors.New("failed to parse certificate").Base(err)
	}
	certificate.Certificate = cert
	certificate.CertificatePath = c.CertFile

	if len(c.KeyFile) > 0 || len(c.KeyStr) > 0 {
		key, err := readFileOrString(c.KeyFile, c.KeyStr)
		if err != nil {
			return nil, errors.New("failed to parse key").Base(err)
		}
		certificate.Key = key
		certificate.KeyPath = c.KeyFile
	}

	switch strings.ToLower(c.Usage) {
	case "encipherment":
		certificate.Usage = tls.Certificate_ENCIPHERMENT
	case "verify":
		certificate.Usage = tls.Certificate_AUTHORITY_VERIFY
	case "issue":
		certificate.Usage = tls.Certificate_AUTHORITY_ISSUE
	default:
		certificate.Usage = tls.Certificate_ENCIPHERMENT
	}
	if certificate.KeyPath == "" && certificate.CertificatePath == "" {
		certificate.OneTimeLoading = true
	} else {
		certificate.OneTimeLoading = c.OneTimeLoading
	}
	certificate.OcspStapling = c.OcspStapling
	certificate.BuildChain = c.BuildChain

	return certificate, nil
}

type TLSConfig struct {
	AllowInsecure           bool             `json:"allowInsecure"`
	Certs                   []*TLSCertConfig `json:"certificates"`
	ServerName              string           `json:"serverName"`
	ALPN                    *StringList      `json:"alpn"`
	EnableSessionResumption bool             `json:"enableSessionResumption"`
	DisableSystemRoot       bool             `json:"disableSystemRoot"`
	MinVersion              string           `json:"minVersion"`
	MaxVersion              string           `json:"maxVersion"`
	CipherSuites            string           `json:"cipherSuites"`
	Fingerprint             string           `json:"fingerprint"`
	RejectUnknownSNI        bool             `json:"rejectUnknownSni"`
	CurvePreferences        *StringList      `json:"curvePreferences"`
	MasterKeyLog            string           `json:"masterKeyLog"`
	PinnedPeerCertSha256    string           `json:"pinnedPeerCertSha256"`
	VerifyPeerCertByName    string           `json:"verifyPeerCertByName"`
	VerifyPeerCertInNames   []string         `json:"verifyPeerCertInNames"`
	ECHServerKeys           string           `json:"echServerKeys"`
	ECHConfigList           string           `json:"echConfigList"`
	ECHForceQuery           string           `json:"echForceQuery"`
	ECHSocketSettings       *SocketConfig    `json:"echSockopt"`
}

// Build implements Buildable.
func (c *TLSConfig) Build() (proto.Message, error) {
	config := new(tls.Config)
	config.Certificate = make([]*tls.Certificate, len(c.Certs))
	for idx, certConf := range c.Certs {
		cert, err := certConf.Build()
		if err != nil {
			return nil, err
		}
		config.Certificate[idx] = cert
	}
	serverName := c.ServerName
	if len(c.ServerName) > 0 {
		config.ServerName = serverName
	}
	if c.ALPN != nil && len(*c.ALPN) > 0 {
		config.NextProtocol = []string(*c.ALPN)
	}
	if len(config.NextProtocol) > 1 {
		for _, p := range config.NextProtocol {
			if tls.IsFromMitm(p) {
				return nil, errors.New(`only one element is allowed in "alpn" when using "fromMitm" in it`)
			}
		}
	}
	if c.CurvePreferences != nil && len(*c.CurvePreferences) > 0 {
		config.CurvePreferences = []string(*c.CurvePreferences)
	}
	config.EnableSessionResumption = c.EnableSessionResumption
	config.DisableSystemRoot = c.DisableSystemRoot
	config.MinVersion = c.MinVersion
	config.MaxVersion = c.MaxVersion
	config.CipherSuites = c.CipherSuites
	config.Fingerprint = strings.ToLower(c.Fingerprint)
	if config.Fingerprint != "unsafe" && tls.GetFingerprint(config.Fingerprint) == nil {
		return nil, errors.New(`unknown "fingerprint": `, config.Fingerprint)
	}
	config.RejectUnknownSni = c.RejectUnknownSNI
	config.MasterKeyLog = c.MasterKeyLog

	if c.AllowInsecure {
		if time.Now().After(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
			return nil, errors.PrintRemovedFeatureError(`"allowInsecure"`, `"pinnedPeerCertSha256"`)
		} else {
			errors.LogWarning(context.Background(), `"allowInsecure" will be removed automatically after 2026-06-01, please use "pinnedPeerCertSha256"(pcs) and "verifyPeerCertByName"(vcn) instead, PLEASE CONTACT YOUR SERVICE PROVIDER (AIRPORT)`)
			config.AllowInsecure = true
		}
	}
	if c.PinnedPeerCertSha256 != "" {
		for v := range strings.SplitSeq(c.PinnedPeerCertSha256, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			// remove colons for OpenSSL format
			hashValue, err := hex.DecodeString(strings.ReplaceAll(v, ":", ""))
			if err != nil {
				return nil, err
			}
			if len(hashValue) != 32 {
				return nil, errors.New("incorrect pinnedPeerCertSha256 length: ", v)
			}
			config.PinnedPeerCertSha256 = append(config.PinnedPeerCertSha256, hashValue)
		}
	}

	if c.VerifyPeerCertInNames != nil {
		return nil, errors.PrintRemovedFeatureError(`"verifyPeerCertInNames"`, `"verifyPeerCertByName"`)
	}
	if c.VerifyPeerCertByName != "" {
		for v := range strings.SplitSeq(c.VerifyPeerCertByName, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			config.VerifyPeerCertByName = append(config.VerifyPeerCertByName, v)
		}
	}

	if c.ECHServerKeys != "" {
		EchPrivateKey, err := base64.StdEncoding.DecodeString(c.ECHServerKeys)
		if err != nil {
			return nil, errors.New("invalid ECH Config", c.ECHServerKeys)
		}
		config.EchServerKeys = EchPrivateKey
	}
	switch c.ECHForceQuery {
	case "none", "half", "full", "":
		config.EchForceQuery = c.ECHForceQuery
	default:
		return nil, errors.New(`invalid "echForceQuery": `, c.ECHForceQuery)
	}
	config.EchForceQuery = c.ECHForceQuery
	config.EchConfigList = c.ECHConfigList
	if c.ECHSocketSettings != nil {
		ss, err := c.ECHSocketSettings.Build()
		if err != nil {
			return nil, errors.New("Failed to build ech sockopt.").Base(err)
		}
		config.EchSocketSettings = ss
	}

	return config, nil
}

type LimitFallback struct {
	AfterBytes       uint64
	BytesPerSec      uint64
	BurstBytesPerSec uint64
}

type REALITYConfig struct {
	MasterKeyLog string          `json:"masterKeyLog"`
	Show         bool            `json:"show"`
	Target       json.RawMessage `json:"target"`
	Dest         json.RawMessage `json:"dest"`
	Type         string          `json:"type"`
	Xver         uint64          `json:"xver"`
	ServerNames  []string        `json:"serverNames"`
	PrivateKey   string          `json:"privateKey"`
	MinClientVer string          `json:"minClientVer"`
	MaxClientVer string          `json:"maxClientVer"`
	MaxTimeDiff  uint64          `json:"maxTimeDiff"`
	ShortIds     []string        `json:"shortIds"`
	Mldsa65Seed  string          `json:"mldsa65Seed"`

	LimitFallbackUpload   LimitFallback `json:"limitFallbackUpload"`
	LimitFallbackDownload LimitFallback `json:"limitFallbackDownload"`

	Fingerprint   string `json:"fingerprint"`
	ServerName    string `json:"serverName"`
	Password      string `json:"password"`
	PublicKey     string `json:"publicKey"`
	ShortId       string `json:"shortId"`
	Mldsa65Verify string `json:"mldsa65Verify"`
	SpiderX       string `json:"spiderX"`
}

func (c *REALITYConfig) Build() (proto.Message, error) {
	config := new(reality.Config)
	config.MasterKeyLog = c.MasterKeyLog
	config.Show = c.Show
	var err error
	if c.Target != nil {
		c.Dest = c.Target
	}
	if c.Dest != nil {
		var i uint16
		var s string
		if err = json.Unmarshal(c.Dest, &i); err == nil {
			s = strconv.Itoa(int(i))
		} else {
			_ = json.Unmarshal(c.Dest, &s)
		}
		if c.Type == "" && s != "" {
			switch s[0] {
			case '@', '/':
				c.Type = "unix"
				if s[0] == '@' && len(s) > 1 && s[1] == '@' && (runtime.GOOS == "linux" || runtime.GOOS == "android") {
					fullAddr := make([]byte, len(syscall.RawSockaddrUnix{}.Path)) // may need padding to work with haproxy
					copy(fullAddr, s[1:])
					s = string(fullAddr)
				}
			default:
				if _, err = strconv.Atoi(s); err == nil {
					s = "localhost:" + s
				}
				if _, _, err = net.SplitHostPort(s); err == nil {
					c.Type = "tcp"
				}
			}
		}
		if c.Type == "" {
			return nil, errors.New(`please fill in a valid value for "target"`)
		}
		if c.Xver > 2 {
			return nil, errors.New(`invalid PROXY protocol version, "xver" only accepts 0, 1, 2`)
		}
		if len(c.ServerNames) == 0 {
			return nil, errors.New(`empty "serverNames"`)
		}
		if c.PrivateKey == "" {
			return nil, errors.New(`empty "privateKey"`)
		}
		if config.PrivateKey, err = base64.RawURLEncoding.DecodeString(c.PrivateKey); err != nil || len(config.PrivateKey) != 32 {
			return nil, errors.New(`invalid "privateKey": `, c.PrivateKey)
		}
		if c.MinClientVer != "" {
			config.MinClientVer = make([]byte, 3)
			var u uint64
			for i, s := range strings.Split(c.MinClientVer, ".") {
				if i == 3 {
					return nil, errors.New(`invalid "minClientVer": `, c.MinClientVer)
				}
				if u, err = strconv.ParseUint(s, 10, 8); err != nil {
					return nil, errors.New(`"minClientVer[`, i, `]" should be less than 256`)
				} else {
					config.MinClientVer[i] = byte(u)
				}
			}
		}
		if c.MaxClientVer != "" {
			config.MaxClientVer = make([]byte, 3)
			var u uint64
			for i, s := range strings.Split(c.MaxClientVer, ".") {
				if i == 3 {
					return nil, errors.New(`invalid "maxClientVer": `, c.MaxClientVer)
				}
				if u, err = strconv.ParseUint(s, 10, 8); err != nil {
					return nil, errors.New(`"maxClientVer[`, i, `]" should be less than 256`)
				} else {
					config.MaxClientVer[i] = byte(u)
				}
			}
		}
		if len(c.ShortIds) == 0 {
			return nil, errors.New(`empty "shortIds"`)
		}
		config.ShortIds = make([][]byte, len(c.ShortIds))
		for i, s := range c.ShortIds {
			if len(s) > 16 {
				return nil, errors.New(`too long "shortIds[`, i, `]": `, s)
			}
			config.ShortIds[i] = make([]byte, 8)
			if _, err = hex.Decode(config.ShortIds[i], []byte(s)); err != nil {
				return nil, errors.New(`invalid "shortIds[`, i, `]": `, s)
			}
		}
		config.Dest = s
		config.Type = c.Type
		config.Xver = c.Xver
		config.ServerNames = c.ServerNames
		config.MaxTimeDiff = c.MaxTimeDiff

		if c.Mldsa65Seed != "" {
			if c.Mldsa65Seed == c.PrivateKey {
				return nil, errors.New(`"mldsa65Seed" and "privateKey" can not be the same value: `, c.Mldsa65Seed)
			}
			if config.Mldsa65Seed, err = base64.RawURLEncoding.DecodeString(c.Mldsa65Seed); err != nil || len(config.Mldsa65Seed) != 32 {
				return nil, errors.New(`invalid "mldsa65Seed": `, c.Mldsa65Seed)
			}
		}

		for _, sn := range config.ServerNames {
			if strings.Contains(sn, "apple") || strings.Contains(sn, "icloud") {
				errors.LogWarning(context.Background(), `REALITY: Choosing apple, icloud, etc. as the target may get your IP blocked by the GFW`)
			}
		}

		config.LimitFallbackUpload = new(reality.LimitFallback)
		config.LimitFallbackUpload.AfterBytes = c.LimitFallbackUpload.AfterBytes
		config.LimitFallbackUpload.BytesPerSec = c.LimitFallbackUpload.BytesPerSec
		config.LimitFallbackUpload.BurstBytesPerSec = c.LimitFallbackUpload.BurstBytesPerSec
		config.LimitFallbackDownload = new(reality.LimitFallback)
		config.LimitFallbackDownload.AfterBytes = c.LimitFallbackDownload.AfterBytes
		config.LimitFallbackDownload.BytesPerSec = c.LimitFallbackDownload.BytesPerSec
		config.LimitFallbackDownload.BurstBytesPerSec = c.LimitFallbackDownload.BurstBytesPerSec
	} else {
		config.Fingerprint = strings.ToLower(c.Fingerprint)
		if config.Fingerprint == "unsafe" || config.Fingerprint == "hellogolang" {
			return nil, errors.New(`invalid "fingerprint": `, config.Fingerprint)
		}
		if tls.GetFingerprint(config.Fingerprint) == nil {
			return nil, errors.New(`unknown "fingerprint": `, config.Fingerprint)
		}
		if len(c.ServerNames) != 0 {
			return nil, errors.New(`non-empty "serverNames", please use "serverName" instead`)
		}
		if c.Password != "" {
			c.PublicKey = c.Password
		}
		if c.PublicKey == "" {
			return nil, errors.New(`empty "password"`)
		}
		if config.PublicKey, err = base64.RawURLEncoding.DecodeString(c.PublicKey); err != nil || len(config.PublicKey) != 32 {
			return nil, errors.New(`invalid "password": `, c.PublicKey)
		}
		if len(c.ShortIds) != 0 {
			return nil, errors.New(`non-empty "shortIds", please use "shortId" instead`)
		}
		if len(c.ShortId) > 16 {
			return nil, errors.New(`too long "shortId": `, c.ShortId)
		}
		config.ShortId = make([]byte, 8)
		if _, err = hex.Decode(config.ShortId, []byte(c.ShortId)); err != nil {
			return nil, errors.New(`invalid "shortId": `, c.ShortId)
		}
		if c.Mldsa65Verify != "" {
			if config.Mldsa65Verify, err = base64.RawURLEncoding.DecodeString(c.Mldsa65Verify); err != nil || len(config.Mldsa65Verify) != 1952 {
				return nil, errors.New(`invalid "mldsa65Verify": `, c.Mldsa65Verify)
			}
		}
		if c.SpiderX == "" {
			c.SpiderX = "/"
		}
		if c.SpiderX[0] != '/' {
			return nil, errors.New(`invalid "spiderX": `, c.SpiderX)
		}
		config.SpiderY = make([]int64, 10)
		u, _ := url.Parse(c.SpiderX)
		q := u.Query()
		parse := func(param string, index int) {
			if q.Get(param) != "" {
				s := strings.Split(q.Get(param), "-")
				if len(s) == 1 {
					config.SpiderY[index], _ = strconv.ParseInt(s[0], 10, 64)
					config.SpiderY[index+1], _ = strconv.ParseInt(s[0], 10, 64)
				} else {
					config.SpiderY[index], _ = strconv.ParseInt(s[0], 10, 64)
					config.SpiderY[index+1], _ = strconv.ParseInt(s[1], 10, 64)
				}
			}
			q.Del(param)
		}
		parse("p", 0) // padding
		parse("c", 2) // concurrency
		parse("t", 4) // times
		parse("i", 6) // interval
		parse("r", 8) // return
		u.RawQuery = q.Encode()
		config.SpiderX = u.String()
		config.ServerName = c.ServerName
	}
	return config, nil
}

type TransportProtocol string

// Build implements Buildable.
func (p TransportProtocol) Build() (string, error) {
	switch strings.ToLower(string(p)) {
	case "raw", "tcp":
		return "tcp", nil
	case "xhttp", "splithttp":
		return "splithttp", nil
	case "kcp", "mkcp":
		return "mkcp", nil
	case "grpc":
		errors.PrintNonRemovalDeprecatedFeatureWarning("gRPC transport (with unnecessary costs, etc.)", "XHTTP stream-up H2")
		return "grpc", nil
	case "ws", "websocket":
		errors.PrintNonRemovalDeprecatedFeatureWarning("WebSocket transport (with ALPN http/1.1, etc.)", "XHTTP H2 & H3")
		return "websocket", nil
	case "httpupgrade":
		errors.PrintNonRemovalDeprecatedFeatureWarning("HTTPUpgrade transport (with ALPN http/1.1, etc.)", "XHTTP H2 & H3")
		return "httpupgrade", nil
	case "h2", "h3", "http":
		return "", errors.PrintRemovedFeatureError("HTTP transport (without header padding, etc.)", "XHTTP stream-one H2 & H3")
	case "quic":
		return "", errors.PrintRemovedFeatureError("QUIC transport (without web service, etc.)", "XHTTP stream-one H3")
	case "hysteria":
		return "hysteria", nil
	default:
		return "", errors.New("Config: unknown transport protocol: ", p)
	}
}

type CustomSockoptConfig struct {
	Syetem  string `json:"system"`
	Network string `json:"network"`
	Level   string `json:"level"`
	Opt     string `json:"opt"`
	Value   string `json:"value"`
	Type    string `json:"type"`
}

type HappyEyeballsConfig struct {
	PrioritizeIPv6   bool   `json:"prioritizeIPv6"`
	TryDelayMs       uint64 `json:"tryDelayMs"`
	Interleave       uint32 `json:"interleave"`
	MaxConcurrentTry uint32 `json:"maxConcurrentTry"`
}

func (h *HappyEyeballsConfig) UnmarshalJSON(data []byte) error {
	var innerHappyEyeballsConfig = struct {
		PrioritizeIPv6   bool   `json:"prioritizeIPv6"`
		TryDelayMs       uint64 `json:"tryDelayMs"`
		Interleave       uint32 `json:"interleave"`
		MaxConcurrentTry uint32 `json:"maxConcurrentTry"`
	}{PrioritizeIPv6: false, Interleave: 1, TryDelayMs: 0, MaxConcurrentTry: 4}
	if err := json.Unmarshal(data, &innerHappyEyeballsConfig); err != nil {
		return err
	}
	h.PrioritizeIPv6 = innerHappyEyeballsConfig.PrioritizeIPv6
	h.TryDelayMs = innerHappyEyeballsConfig.TryDelayMs
	h.Interleave = innerHappyEyeballsConfig.Interleave
	h.MaxConcurrentTry = innerHappyEyeballsConfig.MaxConcurrentTry
	return nil
}

type SocketConfig struct {
	Mark                  int32                  `json:"mark"`
	TFO                   interface{}            `json:"tcpFastOpen"`
	TProxy                string                 `json:"tproxy"`
	AcceptProxyProtocol   bool                   `json:"acceptProxyProtocol"`
	DomainStrategy        string                 `json:"domainStrategy"`
	DialerProxy           string                 `json:"dialerProxy"`
	TCPKeepAliveInterval  int32                  `json:"tcpKeepAliveInterval"`
	TCPKeepAliveIdle      int32                  `json:"tcpKeepAliveIdle"`
	TCPCongestion         string                 `json:"tcpCongestion"`
	TCPWindowClamp        int32                  `json:"tcpWindowClamp"`
	TCPMaxSeg             int32                  `json:"tcpMaxSeg"`
	Penetrate             bool                   `json:"penetrate"`
	TCPUserTimeout        int32                  `json:"tcpUserTimeout"`
	V6only                bool                   `json:"v6only"`
	Interface             string                 `json:"interface"`
	TcpMptcp              bool                   `json:"tcpMptcp"`
	CustomSockopt         []*CustomSockoptConfig `json:"customSockopt"`
	AddressPortStrategy   string                 `json:"addressPortStrategy"`
	HappyEyeballsSettings *HappyEyeballsConfig   `json:"happyEyeballs"`
	TrustedXForwardedFor  []string               `json:"trustedXForwardedFor"`
}

// Build implements Buildable.
func (c *SocketConfig) Build() (*internet.SocketConfig, error) {
	tfo := int32(0) // don't invoke setsockopt() for TFO
	if c.TFO != nil {
		switch v := c.TFO.(type) {
		case bool:
			if v {
				tfo = 256
			} else {
				tfo = -1 // TFO need to be disabled
			}
		case float64:
			tfo = int32(math.Min(v, math.MaxInt32))
		default:
			return nil, errors.New("tcpFastOpen: only boolean and integer value is acceptable")
		}
	}
	var tproxy internet.SocketConfig_TProxyMode
	switch strings.ToLower(c.TProxy) {
	case "tproxy":
		tproxy = internet.SocketConfig_TProxy
	case "redirect":
		tproxy = internet.SocketConfig_Redirect
	default:
		tproxy = internet.SocketConfig_Off
	}

	dStrategy := internet.DomainStrategy_AS_IS
	switch strings.ToLower(c.DomainStrategy) {
	case "asis", "":
		dStrategy = internet.DomainStrategy_AS_IS
	case "useip":
		dStrategy = internet.DomainStrategy_USE_IP
	case "useipv4":
		dStrategy = internet.DomainStrategy_USE_IP4
	case "useipv6":
		dStrategy = internet.DomainStrategy_USE_IP6
	case "useipv4v6":
		dStrategy = internet.DomainStrategy_USE_IP46
	case "useipv6v4":
		dStrategy = internet.DomainStrategy_USE_IP64
	case "forceip":
		dStrategy = internet.DomainStrategy_FORCE_IP
	case "forceipv4":
		dStrategy = internet.DomainStrategy_FORCE_IP4
	case "forceipv6":
		dStrategy = internet.DomainStrategy_FORCE_IP6
	case "forceipv4v6":
		dStrategy = internet.DomainStrategy_FORCE_IP46
	case "forceipv6v4":
		dStrategy = internet.DomainStrategy_FORCE_IP64
	default:
		return nil, errors.New("unsupported domain strategy: ", c.DomainStrategy)
	}

	var customSockopts []*internet.CustomSockopt

	for _, copt := range c.CustomSockopt {
		customSockopt := &internet.CustomSockopt{
			System:  copt.Syetem,
			Network: copt.Network,
			Level:   copt.Level,
			Opt:     copt.Opt,
			Value:   copt.Value,
			Type:    copt.Type,
		}
		customSockopts = append(customSockopts, customSockopt)
	}

	addressPortStrategy := internet.AddressPortStrategy_None
	switch strings.ToLower(c.AddressPortStrategy) {
	case "none", "":
		addressPortStrategy = internet.AddressPortStrategy_None
	case "srvportonly":
		addressPortStrategy = internet.AddressPortStrategy_SrvPortOnly
	case "srvaddressonly":
		addressPortStrategy = internet.AddressPortStrategy_SrvAddressOnly
	case "srvportandaddress":
		addressPortStrategy = internet.AddressPortStrategy_SrvPortAndAddress
	case "txtportonly":
		addressPortStrategy = internet.AddressPortStrategy_TxtPortOnly
	case "txtaddressonly":
		addressPortStrategy = internet.AddressPortStrategy_TxtAddressOnly
	case "txtportandaddress":
		addressPortStrategy = internet.AddressPortStrategy_TxtPortAndAddress
	default:
		return nil, errors.New("unsupported address and port strategy: ", c.AddressPortStrategy)
	}

	var happyEyeballs = &internet.HappyEyeballsConfig{Interleave: 1, PrioritizeIpv6: false, TryDelayMs: 0, MaxConcurrentTry: 4}
	if c.HappyEyeballsSettings != nil {
		happyEyeballs.PrioritizeIpv6 = c.HappyEyeballsSettings.PrioritizeIPv6
		happyEyeballs.Interleave = c.HappyEyeballsSettings.Interleave
		happyEyeballs.TryDelayMs = c.HappyEyeballsSettings.TryDelayMs
		happyEyeballs.MaxConcurrentTry = c.HappyEyeballsSettings.MaxConcurrentTry
	}

	return &internet.SocketConfig{
		Mark:                 c.Mark,
		Tfo:                  tfo,
		Tproxy:               tproxy,
		DomainStrategy:       dStrategy,
		AcceptProxyProtocol:  c.AcceptProxyProtocol,
		DialerProxy:          c.DialerProxy,
		TcpKeepAliveInterval: c.TCPKeepAliveInterval,
		TcpKeepAliveIdle:     c.TCPKeepAliveIdle,
		TcpCongestion:        c.TCPCongestion,
		TcpWindowClamp:       c.TCPWindowClamp,
		TcpMaxSeg:            c.TCPMaxSeg,
		Penetrate:            c.Penetrate,
		TcpUserTimeout:       c.TCPUserTimeout,
		V6Only:               c.V6only,
		Interface:            c.Interface,
		TcpMptcp:             c.TcpMptcp,
		CustomSockopt:        customSockopts,
		AddressPortStrategy:  addressPortStrategy,
		HappyEyeballs:        happyEyeballs,
		TrustedXForwardedFor: c.TrustedXForwardedFor,
	}, nil
}

func PraseByteSlice(data json.RawMessage, typ string) ([]byte, error) {
	switch strings.ToLower(typ) {
	case "", "array":
		if len(data) == 0 {
			return data, nil
		}
		var packet []byte
		if err := json.Unmarshal(data, &packet); err != nil {
			return nil, err
		}
		return packet, nil
	case "str":
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return nil, err
		}
		return []byte(str), nil
	case "hex":
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return nil, err
		}
		return hex.DecodeString(str)
	case "base64":
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return nil, err
		}
		return base64.StdEncoding.DecodeString(str)
	default:
		return nil, errors.New("unknown type")
	}
}

var (
	tcpmaskLoader = NewJSONConfigLoader(ConfigCreatorCache{
		"header-custom": func() interface{} { return new(HeaderCustomTCP) },
		"fragment":      func() interface{} { return new(FragmentMask) },
	}, "type", "settings")
)

type NoOpConnectionAuthenticator struct{}

func (NoOpConnectionAuthenticator) Build() (proto.Message, error) {
	return new(noop.ConnectionConfig), nil
}

type TCPItem struct {
	Delay     Int32Range      `json:"delay"`
	Rand      int32           `json:"rand"`
	RandRange *Int32Range     `json:"randRange"`
	Type      string          `json:"type"`
	Packet    json.RawMessage `json:"packet"`
}

type HeaderCustomTCP struct {
	Clients [][]TCPItem `json:"clients"`
	Servers [][]TCPItem `json:"servers"`
	Errors  [][]TCPItem `json:"errors"`
}

func (c *HeaderCustomTCP) Build() (proto.Message, error) {
	for _, value := range c.Clients {
		for _, item := range value {
			if len(item.Packet) > 0 && item.Rand > 0 {
				return nil, errors.New("len(item.Packet) > 0 && item.Rand > 0")
			}
		}
	}
	for _, value := range c.Servers {
		for _, item := range value {
			if len(item.Packet) > 0 && item.Rand > 0 {
				return nil, errors.New("len(item.Packet) > 0 && item.Rand > 0")
			}
		}
	}
	for _, value := range c.Errors {
		for _, item := range value {
			if len(item.Packet) > 0 && item.Rand > 0 {
				return nil, errors.New("len(item.Packet) > 0 && item.Rand > 0")
			}
		}
	}

	errInvalidRange := errors.New("invalid randRange")

	clients := make([]*custom.TCPSequence, len(c.Clients))
	for i, value := range c.Clients {
		clients[i] = &custom.TCPSequence{}
		for _, item := range value {
			if item.RandRange == nil {
				item.RandRange = &Int32Range{From: 0, To: 255}
			}
			if item.RandRange.From < 0 || item.RandRange.To > 255 {
				return nil, errInvalidRange
			}
			var err error
			if item.Packet, err = PraseByteSlice(item.Packet, item.Type); err != nil {
				return nil, err
			}
			clients[i].Sequence = append(clients[i].Sequence, &custom.TCPItem{
				DelayMin: int64(item.Delay.From),
				DelayMax: int64(item.Delay.To),
				Rand:     item.Rand,
				RandMin:  item.RandRange.From,
				RandMax:  item.RandRange.To,
				Packet:   item.Packet,
			})
		}
	}

	servers := make([]*custom.TCPSequence, len(c.Servers))
	for i, value := range c.Servers {
		servers[i] = &custom.TCPSequence{}
		for _, item := range value {
			if item.RandRange == nil {
				item.RandRange = &Int32Range{From: 0, To: 255}
			}
			if item.RandRange.From < 0 || item.RandRange.To > 255 {
				return nil, errInvalidRange
			}
			var err error
			if item.Packet, err = PraseByteSlice(item.Packet, item.Type); err != nil {
				return nil, err
			}
			servers[i].Sequence = append(servers[i].Sequence, &custom.TCPItem{
				DelayMin: int64(item.Delay.From),
				DelayMax: int64(item.Delay.To),
				Rand:     item.Rand,
				RandMin:  item.RandRange.From,
				RandMax:  item.RandRange.To,
				Packet:   item.Packet,
			})
		}
	}

	errors := make([]*custom.TCPSequence, len(c.Errors))
	for i, value := range c.Errors {
		errors[i] = &custom.TCPSequence{}
		for _, item := range value {
			if item.RandRange == nil {
				item.RandRange = &Int32Range{From: 0, To: 255}
			}
			if item.RandRange.From < 0 || item.RandRange.To > 255 {
				return nil, errInvalidRange
			}
			var err error
			if item.Packet, err = PraseByteSlice(item.Packet, item.Type); err != nil {
				return nil, err
			}
			errors[i].Sequence = append(errors[i].Sequence, &custom.TCPItem{
				DelayMin: int64(item.Delay.From),
				DelayMax: int64(item.Delay.To),
				Rand:     item.Rand,
				RandMin:  item.RandRange.From,
				RandMax:  item.RandRange.To,
				Packet:   item.Packet,
			})
		}
	}

	return &custom.TCPConfig{
		Clients: clients,
		Servers: servers,
		Errors:  errors,
	}, nil
}

type FragmentMask struct {
	Packets  string     `json:"packets"`
	Length   Int32Range `json:"length"`
	Delay    Int32Range `json:"delay"`
	MaxSplit Int32Range `json:"maxSplit"`
}

func (c *FragmentMask) Build() (proto.Message, error) {
	config := &fragment.Config{}

	switch strings.ToLower(c.Packets) {
	case "tlshello":
		config.PacketsFrom = 0
		config.PacketsTo = 1
	case "":
		config.PacketsFrom = 0
		config.PacketsTo = 0
	default:
		from, to, err := ParseRangeString(c.Packets)
		if err != nil {
			return nil, errors.New("Invalid PacketsFrom").Base(err)
		}
		config.PacketsFrom = int64(from)
		config.PacketsTo = int64(to)
		if config.PacketsFrom == 0 {
			return nil, errors.New("PacketsFrom can't be 0")
		}
	}

	config.LengthMin = int64(c.Length.From)
	config.LengthMax = int64(c.Length.To)
	if config.LengthMin == 0 {
		return nil, errors.New("LengthMin can't be 0")
	}

	config.DelayMin = int64(c.Delay.From)
	config.DelayMax = int64(c.Delay.To)

	config.MaxSplitMin = int64(c.MaxSplit.From)
	config.MaxSplitMax = int64(c.MaxSplit.To)

	return config, nil
}

type StreamConfig struct {
	Address             *Address           `json:"address"`
	Port                uint16             `json:"port"`
	Network             *TransportProtocol `json:"network"`
	Security            string             `json:"security"`
	TLSSettings         *TLSConfig         `json:"tlsSettings"`
	REALITYSettings     *REALITYConfig     `json:"realitySettings"`
	RAWSettings         *TCPConfig         `json:"rawSettings"`
	TCPSettings         *TCPConfig         `json:"tcpSettings"`
	SocketSettings      *SocketConfig      `json:"sockopt"`
}

// Build implements Buildable.
func (c *StreamConfig) Build() (*internet.StreamConfig, error) {
	config := &internet.StreamConfig{
		Port:         uint32(c.Port),
		ProtocolName: "tcp",
	}
	if c.Address != nil {
		config.Address = c.Address.Build()
	}
	if c.Network != nil {
		protocol, err := c.Network.Build()
		if err != nil {
			return nil, err
		}
		config.ProtocolName = protocol
	}

	switch strings.ToLower(c.Security) {
	case "", "none":
	case "tls":
		tlsSettings := c.TLSSettings
		if tlsSettings == nil {
			tlsSettings = &TLSConfig{}
		}
		ts, err := tlsSettings.Build()
		if err != nil {
			return nil, errors.New("Failed to build TLS config.").Base(err)
		}
		tm := serial.ToTypedMessage(ts)
		config.SecuritySettings = append(config.SecuritySettings, tm)
		config.SecurityType = tm.Type
	case "reality":
		if config.ProtocolName != "tcp" && config.ProtocolName != "splithttp" && config.ProtocolName != "grpc" {
			return nil, errors.New("REALITY only supports RAW, XHTTP and gRPC for now.")
		}
		if c.REALITYSettings == nil {
			return nil, errors.New(`REALITY: Empty "realitySettings".`)
		}
		ts, err := c.REALITYSettings.Build()
		if err != nil {
			return nil, errors.New("Failed to build REALITY config.").Base(err)
		}
		tm := serial.ToTypedMessage(ts)
		config.SecuritySettings = append(config.SecuritySettings, tm)
		config.SecurityType = tm.Type
	case "xtls":
		return nil, errors.PrintRemovedFeatureError(`Legacy XTLS`, `xtls-rprx-vision with TLS or REALITY`)
	default:
		return nil, errors.New(`Unknown security "` + c.Security + `".`)
	}

	if c.RAWSettings != nil {
		c.TCPSettings = c.RAWSettings
	}
	if c.TCPSettings != nil {
		ts, err := c.TCPSettings.Build()
		if err != nil {
			return nil, errors.New("Failed to build RAW config.").Base(err)
		}
		config.TransportSettings = append(config.TransportSettings, &internet.TransportConfig{
			ProtocolName: "tcp",
			Settings:     serial.ToTypedMessage(ts),
		})
	}
	if c.SocketSettings != nil {
		ss, err := c.SocketSettings.Build()
		if err != nil {
			return nil, errors.New("Failed to build sockopt.").Base(err)
		}
		config.SocketSettings = ss
	}


	return config, nil
}

type ProxyConfig struct {
	Tag string `json:"tag"`

	// TransportLayerProxy: For compatibility.
	TransportLayerProxy bool `json:"transportLayer"`
}

// Build implements Buildable.
func (v *ProxyConfig) Build() (*internet.ProxyConfig, error) {
	if v.Tag == "" {
		return nil, errors.New("Proxy tag is not set.")
	}
	return &internet.ProxyConfig{
		Tag:                 v.Tag,
		TransportLayerProxy: v.TransportLayerProxy,
	}, nil
}
