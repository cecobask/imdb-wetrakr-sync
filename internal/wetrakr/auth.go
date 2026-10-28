package wetrakr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// refreshBefore is how long before expiry EnsureToken refreshes the access token.
const refreshBefore = 24 * time.Hour

func (c *Client) currentToken() *Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *Client) setToken(t *Token) error {
	c.mu.Lock()
	c.token = t
	c.mu.Unlock()
	if c.store == nil {
		return nil
	}
	if err := c.store.Save(t); err != nil {
		return fmt.Errorf("failure saving WeTrakr token: %w", err)
	}
	return nil
}

func (c *Client) tokenFromResponse(r tokenResponse) *Token {
	t := &Token{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		TokenType:    r.TokenType,
	}
	if r.NewRefreshToken != "" {
		t.RefreshToken = r.NewRefreshToken
	}
	if r.ExpiresIn > 0 {
		t.ExpiresAt = c.now().Add(time.Duration(r.ExpiresIn) * time.Second).Unix()
	}
	return t
}

// EnsureToken loads the stored token and refreshes it when it expires within a day.
func (c *Client) EnsureToken(ctx context.Context) error {
	if c.currentToken() == nil {
		if c.store == nil {
			return ErrNoToken
		}
		t, err := c.store.Load()
		if err != nil {
			return err
		}
		c.mu.Lock()
		c.token = t
		c.mu.Unlock()
	}
	t := c.currentToken()
	exp := t.expiry()
	if t.AccessToken != "" && (exp.IsZero() || c.now().Add(refreshBefore).Before(exp)) {
		return nil
	}
	c.logger.Info("refreshing WeTrakr access token", "expires_at", exp)
	return c.Refresh(ctx)
}

// Refresh exchanges the refresh token for a new access token. The refresh token rotates, so the new one is saved
// right away.
func (c *Client) Refresh(ctx context.Context) error {
	t := c.currentToken()
	if t == nil || t.RefreshToken == "" {
		return fmt.Errorf("%w: the token has no refresh token", ErrNoToken)
	}
	var resp tokenResponse
	body := map[string]string{"refresh_token": t.RefreshToken}
	if _, err := c.do(ctx, request{method: http.MethodPost, path: "/oauth/token/refresh", body: body}, &resp); err != nil {
		return fmt.Errorf("failure refreshing WeTrakr token (run `iws auth` again if it keeps failing): %w", err)
	}
	if resp.AccessToken == "" {
		return errors.New("wetrakr: refresh response has no access token")
	}
	next := c.tokenFromResponse(resp)
	if next.RefreshToken == "" {
		next.RefreshToken = t.RefreshToken
	}
	return c.setToken(next)
}

// RequestDeviceCode starts the device flow.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	body := map[string]string{"client_id": c.clientID}
	if _, err := c.do(ctx, request{method: http.MethodPost, path: "/oauth/device/code", body: body}, &dc); err != nil {
		return nil, err
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, errors.New("wetrakr: device code response is incomplete")
	}
	return &dc, nil
}

// PollDeviceToken polls until the user approves the device code, then saves and returns the token.
func (c *Client) PollDeviceToken(ctx context.Context, dc *DeviceCode) (*Token, error) {
	interval := time.Duration(max(dc.Interval, 1)) * time.Second
	expiresIn := time.Duration(max(dc.ExpiresIn, 60)) * time.Second
	ctx, cancel := context.WithTimeout(ctx, expiresIn)
	defer cancel()
	body := map[string]string{"code": dc.DeviceCode, "client_id": c.clientID, "client_secret": c.clientSecret}
	r := request{method: http.MethodPost, path: "/oauth/device/token", body: body}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	deadline := c.now().Add(expiresIn)
	lastNotice := c.now()
	for {
		if now := c.now(); now.Sub(lastNotice) >= time.Minute {
			lastNotice = now
			c.logger.Info("waiting for the device code to be approved", "code", dc.UserCode, "remaining", deadline.Sub(now).Round(time.Second))
		}
		if err := c.sleep(ctx, interval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, errors.New("wetrakr: the device code expired before it was approved")
			}
			return nil, err
		}
		status, _, respBody, err := c.send(ctx, r, payload, "")
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			c.logger.Debug("device token poll failed, retrying", "error", err)
			continue
		}
		apiErr := parseError(r.method, r.path, status, respBody)
		switch {
		case status == http.StatusOK:
			var resp tokenResponse
			if err := json.Unmarshal(respBody, &resp); err != nil || resp.AccessToken == "" {
				return nil, fmt.Errorf("wetrakr: invalid device token response: %v", err)
			}
			t := c.tokenFromResponse(resp)
			return t, c.setToken(t)
		case status == http.StatusBadRequest && apiErr.Code != "invalid_request":
			// authorization_pending: the user has not approved the code yet.
		case status == http.StatusTooManyRequests:
			interval += 5 * time.Second
			c.logger.Debug("device token polling too fast, slowing down", "interval", interval)
		case status == http.StatusGone:
			return nil, errors.New("wetrakr: the device code expired before it was approved")
		case status == http.StatusTeapot:
			return nil, errors.New("wetrakr: the authorization was denied")
		case status >= 500:
			c.logger.Debug("device token poll failed, retrying", "error", apiErr)
		default:
			return nil, apiErr
		}
	}
}
