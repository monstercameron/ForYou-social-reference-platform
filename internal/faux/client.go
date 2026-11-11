package faux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Chain is the transfer interface downstream packages use; *Ledger and *Client
// both satisfy it and return the same sentinel errors.
type Chain interface {
	Submit(ctx context.Context, req SubmitRequest) (Transfer, error)
	Transfer(ctx context.Context, network, id string) (Transfer, error)
	Transfers(ctx context.Context, q Query) ([]Transfer, error)
}

// Client accesses a faux ledger over HTTP.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a client for a faux API base URL.
func NewClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: hc}
}

func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, payload)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("faux: response too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		switch response.StatusCode {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
			return fmt.Errorf("%w: %s", ErrInvalid, e.Error)
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrNotFound, e.Error)
		case http.StatusConflict:
			return fmt.Errorf("%w: %s", ErrConflict, e.Error)
		case http.StatusServiceUnavailable:
			return fmt.Errorf("%w: %s", ErrUnavailable, e.Error)
		case http.StatusInsufficientStorage:
			return fmt.Errorf("%w: %s", ErrFull, e.Error)
		}
		return fmt.Errorf("faux: HTTP %d: %s", response.StatusCode, e.Error)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Submit submits a transfer through the HTTP API.
func (c *Client) Submit(ctx context.Context, req SubmitRequest) (Transfer, error) {
	var value Transfer
	err := c.request(ctx, http.MethodPost, "/faux/v1/transfers", req, &value)
	return value, err
}

// Transfer fetches one transfer through the HTTP API.
func (c *Client) Transfer(ctx context.Context, network, id string) (Transfer, error) {
	var value Transfer
	err := c.request(ctx, http.MethodGet, "/faux/v1/transfers/"+url.PathEscape(network)+"/"+url.PathEscape(id), nil, &value)
	return value, err
}

// Transfers lists transfers through the HTTP API.
func (c *Client) Transfers(ctx context.Context, q Query) ([]Transfer, error) {
	params := url.Values{"network": {q.Network}}
	if q.Recipient != "" {
		params.Set("recipient", q.Recipient)
	}
	if q.Data != "" {
		params.Set("data", q.Data)
	}
	if q.After != 0 {
		params.Set("after", strconv.FormatUint(q.After, 10))
	}
	if q.Limit != 0 {
		params.Set("limit", strconv.Itoa(q.Limit))
	}
	var value struct {
		Transfers []Transfer `json:"transfers"`
	}
	err := c.request(ctx, http.MethodGet, "/faux/v1/transfers?"+params.Encode(), nil, &value)
	return value.Transfers, err
}

// Mine appends empty blocks through the HTTP API.
func (c *Client) Mine(ctx context.Context, network string, count int) (Tip, error) {
	var value Tip
	err := c.request(ctx, http.MethodPost, "/faux/v1/blocks", map[string]any{"network": network, "count": count}, &value)
	return value, err
}

// Tip returns the current tip through the HTTP API.
func (c *Client) Tip(ctx context.Context, network string) (Tip, error) {
	var value Tip
	err := c.request(ctx, http.MethodGet, "/faux/v1/chains/"+url.PathEscape(network), nil, &value)
	return value, err
}

// Faucet submits a faucet transfer through the HTTP API.
func (c *Client) Faucet(ctx context.Context, network, recipient, amount string) (Transfer, error) {
	var value Transfer
	err := c.request(ctx, http.MethodPost, "/faux/v1/faucet", map[string]string{"network": network, "recipient": recipient, "amount_chain_units": amount}, &value)
	return value, err
}

// Address returns address totals through the HTTP API.
func (c *Client) Address(ctx context.Context, network, address string) (AddressTotals, error) {
	var value AddressTotals
	err := c.request(ctx, http.MethodGet, "/faux/v1/addresses/"+url.PathEscape(network)+"/"+url.PathEscape(address), nil, &value)
	return value, err
}

var _ Chain = (*Client)(nil)
var _ Chain = (*Ledger)(nil)
