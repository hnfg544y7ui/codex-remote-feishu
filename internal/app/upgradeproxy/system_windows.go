package upgradeproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	winHTTP        = windows.NewLazySystemDLL("winhttp.dll")
	getIEConfig    = winHTTP.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	openSession    = winHTTP.NewProc("WinHttpOpen")
	closeSession   = winHTTP.NewProc("WinHttpCloseHandle")
	getProxyForURL = winHTTP.NewProc("WinHttpGetProxyForUrl")
	setTimeouts    = winHTTP.NewProc("WinHttpSetTimeouts")
	globalFree     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

type ieProxyConfig struct {
	AutoDetect    int32
	AutoConfigURL *uint16
	Proxy         *uint16
	Bypass        *uint16
}

type autoProxyOptions struct {
	Flags           uint32
	AutoDetectFlags uint32
	AutoConfigURL   *uint16
	Reserved        unsafe.Pointer
	ReservedFlags   uint32
	AutoLogon       int32
}

type proxyInfo struct {
	AccessType uint32
	Proxy      *uint16
	Bypass     *uint16
}

func freeSystemString(p *uint16) {
	if p != nil {
		_, _, _ = globalFree.Call(uintptr(unsafe.Pointer(p)))
	}
}

// readSystemSettings uses the logged-on account's Internet Settings, not the
// separate machine-wide netsh WinHTTP proxy. Scheduled tasks run as this user.
func readSystemSettings() (settings, error) {
	var cfg ieProxyConfig
	ok, _, callErr := getIEConfig.Call(uintptr(unsafe.Pointer(&cfg)))
	if ok == 0 {
		return settings{}, fmt.Errorf("read Windows system proxy: %w", callErr)
	}
	defer freeSystemString(cfg.AutoConfigURL)
	defer freeSystemString(cfg.Proxy)
	defer freeSystemString(cfg.Bypass)
	return settings{Proxy: windows.UTF16PtrToString(cfg.Proxy), Bypass: windows.UTF16PtrToString(cfg.Bypass),
		AutoConfigURL: windows.UTF16PtrToString(cfg.AutoConfigURL), AutoDetect: cfg.AutoDetect != 0}, nil
}

func systemProxy(_ []string) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		s, err := readSystemSettings()
		if err != nil {
			return nil, err
		}
		return proxyForSettings(s, req, automaticProxy)
	}
}

func systemEnv(env []string) ([]string, error) {
	s, err := readSystemSettings()
	if err != nil {
		return nil, err
	}
	return automaticEnv(s, env, func() (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.npmjs.org/", nil)
		_, err := automaticProxy(s, req)
		if errors.Is(err, errNoAutoProxy) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return true, nil
	})
}

func automaticProxy(s settings, req *http.Request) (*url.URL, error) {
	userAgent, _ := windows.UTF16PtrFromString("CodexRemoteUpgrade")
	session, _, callErr := openSession.Call(uintptr(unsafe.Pointer(userAgent)), 1, 0, 0, 0)
	if session == 0 {
		return nil, fmt.Errorf("open Windows automatic proxy resolver: %w", callErr)
	}
	defer closeSession.Call(session)
	_, _, _ = setTimeouts.Call(session, 5000, 5000, 5000, 5000)
	var opts autoProxyOptions
	if s.AutoConfigURL != "" {
		opts.Flags = 2 // WINHTTP_AUTOPROXY_CONFIG_URL
		var err error
		opts.AutoConfigURL, err = windows.UTF16PtrFromString(s.AutoConfigURL)
		if err != nil {
			return nil, fmt.Errorf("invalid Windows PAC configuration")
		}
	} else {
		opts.Flags = 1           // WINHTTP_AUTOPROXY_AUTO_DETECT
		opts.AutoDetectFlags = 3 // DHCP | DNS_A
	}
	// Do not send the user's integrated credentials to a PAC endpoint.
	address, err := windows.UTF16PtrFromString(req.URL.String())
	if err != nil {
		return nil, fmt.Errorf("invalid upgrade request URL")
	}
	var info proxyInfo
	ok, _, callErr := getProxyForURL.Call(session, uintptr(unsafe.Pointer(address)), uintptr(unsafe.Pointer(&opts)), uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if s.AutoConfigURL == "" && callErr == syscall.Errno(12180) {
			return nil, errNoAutoProxy
		} // no WPAD deployment
		return nil, fmt.Errorf("resolve Windows automatic proxy: %w", callErr)
	}
	defer freeSystemString(info.Proxy)
	defer freeSystemString(info.Bypass)
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if info.AccessType == 1 {
		return nil, nil
	} // WINHTTP_ACCESS_TYPE_NO_PROXY
	return manualProxy(settings{Proxy: windows.UTF16PtrToString(info.Proxy), Bypass: windows.UTF16PtrToString(info.Bypass)}, req)
}
