package server

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

func sendSQSMessage(ctx context.Context, queueURL, body string) error {
	region := strings.TrimSpace(os.Getenv("AWS_REGION"))
	if region == "" {
		region = "ap-south-1"
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	client := sqs.NewFromConfig(cfg)
	_, err = client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(body),
	})
	return err
}
