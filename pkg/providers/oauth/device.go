package oauth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/diasYuri/y/pkg/providers/auth"
)

func (c Client) device(ctx context.Context) (auth.Credential, error) {
	var device struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		VerificationURL         string `json:"verification_url"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := c.post(ctx, c.Config.DeviceURL, url.Values{"client_id": {c.Config.ClientID}, "scope": {strings.Join(c.Config.Scopes, " ")}}, false, &device); err != nil {
		return auth.Credential{}, err
	}
	if device.DeviceCode == "" || device.ExpiresIn <= 0 {
		return auth.Credential{}, errors.New("oauth: invalid device code response")
	}
	verification := device.VerificationURIComplete
	if verification == "" {
		verification = device.VerificationURI
	}
	if verification == "" {
		verification = device.VerificationURL
	}
	if verification == "" {
		return auth.Credential{}, errors.New("oauth: missing verification URL")
	}
	expires := c.now().Add(time.Duration(device.ExpiresIn) * time.Second)
	if err := c.Notify(ctx, Notice{URL: verification, UserCode: device.UserCode, ExpiresAt: expires}); err != nil {
		return auth.Credential{}, err
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		if !c.now().Add(interval).Before(expires) {
			return auth.Credential{}, errors.New("oauth: device code expired")
		}
		if err := c.wait(ctx, interval); err != nil {
			return auth.Credential{}, err
		}
		if !c.now().Before(expires) {
			return auth.Credential{}, errors.New("oauth: device code expired")
		}
		credential, err := c.token(ctx, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {device.DeviceCode}})
		if err == nil {
			return credential, nil
		}
		var endpoint *endpointError
		if !errors.As(err, &endpoint) {
			return auth.Credential{}, err
		}
		switch endpoint.code {
		case "authorization_pending", "pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		}
		if endpoint.status == 429 {
			if endpoint.retry > interval {
				interval = endpoint.retry
			}
			continue
		}
		return auth.Credential{}, err
	}
}
