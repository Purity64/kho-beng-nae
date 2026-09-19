package protocal

import (
	"fmt"
	"net/http"
	"time"
)

type Protocol uint8

const (
	ProtocolUnknown Protocol = iota
	ProtocolHTTP1
	ProtocolHTTP2
)



func CheckProtocol(target string) (Protocol, error) {
	client := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest(http.MethodHead, target, nil)
	if err != nil {
		return ProtocolUnknown, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return ProtocolUnknown, err
	}
	defer resp.Body.Close()

	switch resp.ProtoMajor {
	case 2:
		return ProtocolHTTP2, nil

	case 1:
		return ProtocolHTTP1, nil

	default:
		return ProtocolUnknown, fmt.Errorf(
			"unsupported HTTP protocol: %s",
			resp.Proto,
		)
	}
}