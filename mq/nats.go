package mq

import (
	"context"
	"fmt"
	"log/slog"

	"clipboard-sync/protocol"
	"github.com/nats-io/nats.go"
)

type Handler func(subject string, payload []byte) error

type Client struct {
	conn   *nats.Conn
	logger *slog.Logger
	subs   []*nats.Subscription
}

func New(url string, logger *slog.Logger) (*Client, error) {
	conn, err := nats.Connect(url,
		nats.Name("clipboard-sync-agent"),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			if sub != nil {
				logger.Error("nats async error", "subject", sub.Subject, "error", err)
				return
			}
			logger.Error("nats async error", "error", err)
		}),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			logger.Warn("nats disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(conn *nats.Conn) {
			logger.Info("nats reconnected", "url", conn.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			logger.Warn("nats connection closed")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
	}
	return &Client{conn: conn, logger: logger}, nil
}

func (c *Client) Publish(subject string, message any) error {
	payload, err := protocol.Encode(message)
	if err != nil {
		return fmt.Errorf("encode message for %s: %w", subject, err)
	}
	if err := c.conn.Publish(subject, payload); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

func (c *Client) Subscribe(subject string, handler Handler) error {
	sub, err := c.conn.Subscribe(subject, func(msg *nats.Msg) {
		if err := handler(msg.Subject, msg.Data); err != nil {
			c.logger.Error("mq handler failed", "subject", msg.Subject, "error", err)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subject, err)
	}
	if subject == protocol.TopicFileChunk || subject == protocol.TopicImageChunk {
		if err := sub.SetPendingLimits(65536, 1024*1024*1024); err != nil {
			return fmt.Errorf("set pending limits for %s: %w", subject, err)
		}
	}
	c.subs = append(c.subs, sub)
	return nil
}

func (c *Client) Flush(ctx context.Context) error {
	return c.conn.FlushWithContext(ctx)
}

func (c *Client) MaxPayload() int64 {
	return c.conn.MaxPayload()
}

func (c *Client) Close() {
	for _, sub := range c.subs {
		_ = sub.Unsubscribe()
	}
	c.conn.Close()
}
