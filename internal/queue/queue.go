// Package queue carries move-evaluation jobs from the api to the engine
// workers over SQS (ElasticMQ locally, same wire protocol).
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Job asks a worker to play the engine move in one game. Ply pins the
// position the job was created for: if the game has moved on by the time a
// worker picks it up (duplicate delivery, races), the worker drops it.
type Job struct {
	GameID string `json:"game_id"`
	Ply    int    `json:"ply"`
	// Kind is empty or "move" for the engine's own reply, and "hint" when the
	// player asked what they should play. A hint runs the same search and
	// publishes the answer without touching the game, so one queue and one
	// worker fleet serve both and a hint cannot corrupt a position.
	Kind string `json:"kind,omitempty"`
}

const (
	KindMove = "move"
	KindHint = "hint"
)

type Message struct {
	Job    Job
	handle string
}

type Client struct {
	sqs      *sqs.Client
	queueURL string
}

// New connects using the standard AWS config chain. SQS_ENDPOINT overrides
// the endpoint for ElasticMQ; SQS_QUEUE_URL names the queue.
func New(ctx context.Context) (*Client, error) {
	queueURL := os.Getenv("SQS_QUEUE_URL")
	if queueURL == "" {
		return nil, fmt.Errorf("SQS_QUEUE_URL not set")
	}
	var opts []func(*config.LoadOptions) error
	if endpoint := os.Getenv("SQS_ENDPOINT"); endpoint != "" {
		// Local ElasticMQ: any static credentials satisfy the signer.
		opts = append(opts,
			config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")),
			config.WithRegion("us-east-1"),
		)
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	cli := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		if endpoint := os.Getenv("SQS_ENDPOINT"); endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	return &Client{sqs: cli, queueURL: queueURL}, nil
}

func (c *Client) Enqueue(ctx context.Context, j Job) error {
	body, _ := json.Marshal(j)
	_, err := c.sqs.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    &c.queueURL,
		MessageBody: aws.String(string(body)),
	})
	return err
}

// Receive long-polls for up to max jobs.
func (c *Client) Receive(ctx context.Context, max int32) ([]Message, error) {
	out, err := c.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            &c.queueURL,
		MaxNumberOfMessages: max,
		WaitTimeSeconds:     10,
	})
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		var j Job
		if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &j); err != nil {
			// Poison message: delete rather than redeliver forever.
			c.delete(ctx, aws.ToString(m.ReceiptHandle))
			continue
		}
		msgs = append(msgs, Message{Job: j, handle: aws.ToString(m.ReceiptHandle)})
	}
	return msgs, nil
}

func (c *Client) Ack(ctx context.Context, m Message) error {
	return c.delete(ctx, m.handle)
}

func (c *Client) delete(ctx context.Context, handle string) error {
	_, err := c.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      &c.queueURL,
		ReceiptHandle: &handle,
	})
	return err
}
