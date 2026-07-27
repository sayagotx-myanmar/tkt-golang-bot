package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// Structs to parse Telegram's incoming JSON
type Update struct {
	Message *Message `json:"message"`
}
type Message struct {
	Chat *Chat  `json:"chat"`
	Text string `json:"text"`
}
type Chat struct {
	ID int64 `json:"id"`
}

// Handler is the serverless entrypoint
func Handler(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests from Telegram
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var update Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// If a message was received, process it
	if update.Message != nil {
		botToken := os.Getenv("TELEGRAM_BOT_TOKEN") // We will set this securely in Vercel later
		apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

		responseText := "I received your message!"
		if update.Message.Text == "/start" {
			responseText = "Welcome to the TKT Prep Bot, powered by Serverless Golang! 🚀"
		}

		// Prepare the JSON payload to send back to Telegram
		replyBody, _ := json.Marshal(map[string]interface{}{
			"chat_id": update.Message.Chat.ID,
			"text":    responseText,
		})

		// Send the POST request to Telegram's API
		http.Post(apiURL, "application/json", bytes.NewBuffer(replyBody))
	}

	// Always return a 200 OK to prevent Telegram from retrying the message
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}