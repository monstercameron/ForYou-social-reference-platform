package sdk

import (
	"context"
	"errors"
	"net/http"

	"foryou.local/foryou/internal/wire"
)

// DeliverWitness submits a signed settlement witness; status is 201 for new and 200 when already held.
func (c *Client) DeliverWitness(ctx context.Context, witness wire.SettlementWitness) (status int, err error) {
	body, err := wire.Encode("SettlementWitness", witness)
	if err != nil {
		return 0, err
	}
	res, err := c.send(ctx, http.MethodPost, "/v1/settlement-witnesses", nil, body, nil, rdObjectLimit)
	if err != nil {
		var publicError *Error
		if errors.As(err, &publicError) {
			return publicError.HTTPStatus, err
		}
		return 0, err
	}
	if res.status != http.StatusCreated && res.status != http.StatusOK {
		return res.status, rdProtocol("unexpected settlement witness status")
	}
	return res.status, nil
}
