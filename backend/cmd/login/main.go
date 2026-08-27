package main

import (
	"context"
	"log"

	"controle-almoxarifado/backend/internal/api"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

func main() {
	api.LoadEnvFile(".env")
	app, err := api.New(context.Background())
	if err != nil {
		log.Fatalf("init login lambda: %v", err)
	}

	lambda.Start(func(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
		if req.RequestContext.HTTP.Method == "GET" {
			return app.Health(ctx, req)
		}
		return app.Login(ctx, req)
	})
}
