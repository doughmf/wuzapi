package main

import (
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type Client struct {
	client *whatsmeow.Client
}

type ClientManager struct {
	clients map[string]*Client
}

var clientManager = &ClientManager{
	clients: make(map[string]*Client),
}

func NewClientManager() *ClientManager {
	return &ClientManager{
		clients: make(map[string]*Client),
	}
}

func (cm *ClientManager) AddClient(id string, client *Client) {
	cm.clients[id] = client
}

func (cm *ClientManager) GetClient(id string) *Client {
	return cm.clients[id]
}

func (cm *ClientManager) GetWhatsmeowClient(id string) *whatsmeow.Client {
	if c, ok := cm.clients[id]; ok {
		return c.client
	}
	return nil
}

func (c *Client) Connect() error {
	if c.client.IsConnected() {
		return nil
	}
	return c.client.Connect()
}

func (c *Client) Disconnect() {
	c.client.Disconnect()
}

// Helper seguro
func shouldIgnoreJID(jid string) bool {
	// Acesso seguro à config
	cwCfgMutex.RLock()
	defer cwCfgMutex.RUnlock()
	for _, ignore := range cwCfg.IgnoreJIDs {
		if strings.Contains(jid, ignore) {
			return true
		}
	}
	return false
}

func (c *Client) EventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		if time.Since(v.Info.Timestamp) > 2*time.Minute {
			return
		}

		go func() {
			if shouldIgnoreJID(v.Info.Chat.String()) {
				return
			}

			senderName := v.Info.PushName
			if senderName == "" {
				senderName = strings.Split(v.Info.Sender.String(), "@")[0]
			}
			senderPhone := v.Info.Sender.String()

			// EXTRAÇÃO DE TEXTO SEGURA
			text := ""
			if v.Message.Conversation != nil {
				text = *v.Message.Conversation
			} else if v.Message.ExtendedTextMessage != nil {
				text = *v.Message.ExtendedTextMessage.Text
			}

			if text != "" {
				SendToChatwoot(senderName, senderPhone, text)
			}
		}()

		go c.HandleWebhook(v)
	}
}

func (c *Client) HandleWebhook(v *events.Message) {
	webhookURL := os.Getenv("WUZAPI_WEBHOOK_URL")
	if webhookURL == "" {
		return
	}
	// Lógica original...
}

func NewClient(deviceStore *sqlstore.Device, logger waLog.Logger) *Client {
	c := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "INFO", true))
	client := &Client{
		client: c,
	}
	c.AddEventHandler(client.EventHandler)
	return client
}
