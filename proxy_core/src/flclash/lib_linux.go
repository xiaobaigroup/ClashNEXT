//go:build ohos && cgo

package main

import "C"
import (
	"core/platform"
	"core/state"
	t "core/tun"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/process"
	"github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/listener/sing_tun"
	"github.com/metacubex/mihomo/log"
)

type ProcessMap struct {
	m sync.Map
}

type FdMap struct {
	m sync.Map
}

type Fd struct {
	Id    int64 `json:"id"`
	Value int64 `json:"value"`
}

var (
	tunListener *sing_tun.Listener
	fdMap       FdMap
	fdCounter   int64 = 0
	counter     int64 = 0
	processMap  ProcessMap
	tunLock     sync.Mutex
	runTime     *time.Time
	errBlocked  = errors.New("blocked")
)

func (cm *ProcessMap) Store(key int64, value string) {
	cm.m.Store(key, value)
}

func (cm *ProcessMap) Load(key int64) (string, bool) {
	value, ok := cm.m.Load(key)
	if !ok || value == nil {
		return "", false
	}
	return value.(string), true
}

func (cm *FdMap) Store(key int64) {
	cm.m.Store(key, struct{}{})
}

func (cm *FdMap) Load(key int64) bool {
	_, ok := cm.m.Load(key)
	return ok
}

func StartTUN(fd int, markSocket func(Fd)) {
	if fd == 0 {
		tunLock.Lock()
		defer tunLock.Unlock()
		now := time.Now()
		runTime = &now
		// SendMessage(Message{
		// 	Type: StartedMessage,
		// 	Data: strconv.FormatInt(runTime.UnixMilli(), 10),
		// })
		return
	}
	initSocketHook(markSocket)
	go func() {
		tunLock.Lock()
		defer tunLock.Unlock()
		f := int(fd)
		tunListener, _ = t.Start(f, currentConfig.General.Tun.Device, currentConfig.General.Tun.Stack)
		if tunListener != nil {
			log.Infoln("TUN address: %v", tunListener.Address())
		}
		now := time.Now()
		runTime = &now
	}()
}

func GetRunTime() *C.char {
	if runTime == nil {
		return C.CString("")
	}
	return C.CString(strconv.FormatInt(runTime.UnixMilli(), 10))
}

func StopTun() {
	go func() {
		tunLock.Lock()
		defer tunLock.Unlock()

		runTime = nil

		if tunListener != nil {
			_ = tunListener.Close()
		}
		removeSocketHook()
	}()
}

func SetFdMap(fd C.long) {
	fdInt := int64(fd)
	go func() {
		fdMap.Store(fdInt)
	}()
}

func initSocketHook(markSocket func(Fd)) {
	dialer.DefaultSocketHook = func(network, address string, conn syscall.RawConn) error {
		if platform.ShouldBlockConnection() {
			return errBlocked
		}
		return conn.Control(func(fd uintptr) {
			fdInt := int64(fd)
			timeout := time.After(500 * time.Millisecond)
			id := atomic.AddInt64(&fdCounter, 1)

			markSocket(Fd{
				Id:    id,
				Value: fdInt,
			})

			for {
				select {
				case <-timeout:
					return
				default:
					exists := fdMap.Load(id)
					if exists {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
		})
	}
}

func removeSocketHook() {
	dialer.DefaultSocketHook = nil
}

func init() {
	process.DefaultPackageNameResolver = func(metadata *constant.Metadata) (string, error) {
		if metadata == nil {
			return "", process.ErrInvalidNetwork
		}
		id := atomic.AddInt64(&counter, 1)

		timeout := time.After(200 * time.Millisecond)

		// SendMessage(Message{
		// 	Type: ProcessMessage,
		// 	Data: Process{
		// 		Id:       id,
		// 		Metadata: metadata,
		// 	},
		// })

		for {
			select {
			case <-timeout:
				return "", errors.New("package resolver timeout")
			default:
				value, exists := processMap.Load(id)
				if exists {
					return value, nil
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
}

func SetProcessMap(s string) string {
	paramsString := s
	go func() {
		var processMapItem = &ProcessMapItem{}
		err := json.Unmarshal([]byte(paramsString), processMapItem)
		if err == nil {
			processMap.Store(processMapItem.Id, processMapItem.Value)
		}
	}()
	return ""
}

func GetCurrentProfileName() string {
	if state.CurrentState == nil {
		return ""
	}
	return state.CurrentState.CurrentProfileName
}

func GetVpnOptions() string {
	tunLock.Lock()
	defer tunLock.Unlock()
	options := state.AndroidVpnOptions{
		Enable:           state.CurrentState.Enable,
		Port:             currentConfig.General.MixedPort,
		Ipv4Address:      state.DefaultIpv4Address,
		Ipv6Address:      state.GetIpv6Address(),
		AccessControl:    state.CurrentState.AccessControl,
		SystemProxy:      state.CurrentState.SystemProxy,
		AllowBypass:      state.CurrentState.AllowBypass,
		RouteAddress:     state.CurrentState.RouteAddress,
		BypassDomain:     state.CurrentState.BypassDomain,
		DnsServerAddress: state.GetDnsServerAddress(),
	}
	data, err := json.Marshal(options)
	if err != nil {
		fmt.Println("Error:", err)
		return ""
	}
	return string(data)
}

func SetState(s *C.char) {
	paramsString := C.GoString(s)
	err := json.Unmarshal([]byte(paramsString), state.CurrentState)
	if err != nil {
		return
	}
}

func UpdateDns(s *C.char) {
	dnsList := C.GoString(s)
	go func() {
		log.Infoln("[DNS] updateDns %s", dnsList)
		dns.UpdateSystemDNS(strings.Split(dnsList, ","))
		dns.FlushCacheWithDefaultResolver()
	}()
}
