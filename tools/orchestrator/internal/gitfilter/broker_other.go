//go:build !darwin

package gitfilter

import (
	"context"
	"errors"
)

func OpenBroker(context.Context, BrokerRequest) (ResourceBroker, error) {
	return nil, errors.New("filter_platform_unsupported")
}
