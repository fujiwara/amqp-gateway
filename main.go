package gateway

import (
	"context"
	"fmt"
)

func Run(ctx context.Context) error {
	fmt.Println("amqp-gateway!")
	return nil
}
