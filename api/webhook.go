package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// ==========================================
// 1. DATA MODELS (STRUCTS)
// ==========================================

type TKTQuestion struct {
	ID            int
	QuestionText  string
	CorrectOption string
	WrongOption1  string
	WrongOption2  string
	Explanation   string
}

type Update struct {
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"` // NEW: Listens for button clicks
}

type Message struct {
	Chat *Chat  `json:"chat"`
	Text string `json:"text"`
}

type Chat struct {
	ID int64 `json:"id"`
}

// NEW: Struct to handle the data when a button is pressed
type CallbackQuery struct {
	ID      string   `json:"id"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// ==========================================
// 2. DATABASE CONNECTION LOGIC
// ==========================================

var db *sql.DB

func initDB() {
	if db != nil {
		return
	}
	connStr := os.Getenv("DATABASE_URL")
	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Printf("Error opening database structure: %v\n", err)
		return
	}
	if err = db.Ping(); err != nil {
		log.Printf("Error pinging Supabase: %v\n", err)
	} else {
		log.Println("Successfully connected to Supabase!")
	}
}

// ==========================================
// 3. MAIN WEBHOOK HANDLER
// ==========================================

func Handler(w http.ResponseWriter, r *http.Request) {
	initDB()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var update Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/", botToken)

	// ==========================================
	// SCENARIO A: THE USER CLICKED A BUTTON
	// ==========================================
	if update.CallbackQuery != nil {
		// 1. Tell Telegram we received the click (stops the loading spinner on the button)
		http.Post(apiURL+"answerCallbackQuery", "application/json", bytes.NewBuffer([]byte(fmt.Sprintf(`{"callback_query_id": "%s"}`, update.CallbackQuery.ID))))

		// 2. Parse the hidden data we attached to the button (e.g., "ans:correct:1")
		dataParts := strings.Split(update.CallbackQuery.Data, ":")
		if len(dataParts) == 3 && dataParts[0] == "ans" 
			{
			status := dataParts[1]
			questionID := dataParts[2]
			} else if update.CallbackQuery.Data == "cmd:next"
			{
			// Trigger the same logic as typing /test
			update.Message = update.CallbackQuery.Message
			update.Message.Text = "/test"
			// The code will naturally flow into Scenario B
			}
			// 3. Fetch the explanation for this specific question from Supabase
			var explanation string
			db.QueryRow("SELECT explanation FROM questions WHERE id = $1", questionID).Scan(&explanation)

			// 4. Formulate the response based on whether they were right or wrong
			var responseText string
			if status == "correct" {
				responseText = "✅ *Correct!*\n\n"
			} else {
				responseText = "❌ *Incorrect!*\n\n"
			}
			responseText += "*Explanation:*\n" + explanation

			// 5. Build a "Next Question" button
			nextKeyboard := InlineKeyboardMarkup{
				InlineKeyboard: [][]InlineKeyboardButton{
					{{Text: "Next Question ➡️", CallbackData: "cmd:next"}},
				},
			}

			// 6. Send the feedback message back to the candidate
			replyBody, _ := json.Marshal(map[string]interface{}{
				"chat_id":      update.CallbackQuery.Message.Chat.ID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})
	} else if update.Message != nil {
		// ==========================================
		// SCENARIO B: THE USER TYPED A MESSAGE
		// ==========================================
		responseText := "I received your message! Type /test to try a question."
		var replyMarkup interface{} = nil // Optional keyboard

		if update.Message.Text == "/start" {
			responseText = "Welcome to the TKT Prep Bot! 🚀\n\nType /test to practice a question."
		} else if update.Message.Text == "/test" {

		var q TKTQuestion
			err := db.QueryRow(`
				SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2, explanation 
				FROM questions 
				ORDER BY RANDOM() 
				LIMIT 1`).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2, &q.Explanation)

			if err != nil {
				responseText = "Database error: " + err.Error()
			} else {
				responseText = fmt.Sprintf("📚 *Question:*\n%s", q.QuestionText)

				// 1. Group the answers and attach secret data to them
				type Option struct {
					Text string
					Data string
				}
				options := []Option{
					{Text: q.CorrectOption, Data: fmt.Sprintf("ans:correct:%d", q.ID)},
					{Text: q.WrongOption1, Data: fmt.Sprintf("ans:wrong:%d", q.ID)},
					{Text: q.WrongOption2, Data: fmt.Sprintf("ans:wrong:%d", q.ID)},
				}

				// 2. Shuffle the answers randomly
				randSource := rand.NewSource(time.Now().UnixNano())
				rander := rand.New(randSource)
				rander.Shuffle(len(options), func(i, j int) {
					options[i], options[j] = options[j], options[i]
				})

				// 3. Build the clickable keyboard layout (one button per row)
				var keyboard [][]InlineKeyboardButton
				for _, opt := range options {
					keyboard = append(keyboard, []InlineKeyboardButton{{Text: opt.Text, CallbackData: opt.Data}})
				}
				replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
			}
		}

		// Send the message (with or without buttons depending on the command)
		payload := map[string]interface{}{
			"chat_id":    update.Message.Chat.ID,
			"text":       responseText,
			"parse_mode": "Markdown",
		}
		if replyMarkup != nil {
			payload["reply_markup"] = replyMarkup
		}

		replyBody, _ := json.Marshal(payload)
		http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}