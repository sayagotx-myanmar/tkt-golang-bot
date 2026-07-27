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
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Message struct {
	Chat *Chat  `json:"chat"`
	Text string `json:"text"`
}

type Chat struct {
	ID int64 `json:"id"`
}

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
		log.Printf("Error opening database: %v\n", err)
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
		// Tell Telegram we received the click (stops the loading animation)
		http.Post(apiURL+"answerCallbackQuery", "application/json", bytes.NewBuffer([]byte(fmt.Sprintf(`{"callback_query_id": "%s"}`, update.CallbackQuery.ID))))

		dataParts := strings.Split(update.CallbackQuery.Data, ":")
		
		// EVENT 1: User chose a category (or clicked "Next Question")
		if dataParts[0] == "cat" && len(dataParts) == 2 {
			category := dataParts[1]
			
			var q TKTQuestion
			err := db.QueryRow(`
				SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2, explanation 
				FROM questions 
				WHERE category = $1 
				ORDER BY RANDOM() 
				LIMIT 1`, category).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2, &q.Explanation)

			var responseText string
			var replyMarkup interface{} = nil

			if err != nil {
				responseText = "No questions found for this category yet!"
			} else {
				responseText = fmt.Sprintf("📚 *Topic: %s*\n\n%s", category, q.QuestionText)

				type Option struct {
					Text string
					Data string
				}
				
				options := []Option{
					{Text: q.CorrectOption, Data: fmt.Sprintf("ans:correct:%d:%s", q.ID, category)},
					{Text: q.WrongOption1, Data: fmt.Sprintf("ans:wrong:%d:%s", q.ID, category)},
					{Text: q.WrongOption2, Data: fmt.Sprintf("ans:wrong:%d:%s", q.ID, category)},
				}

				randSource := rand.NewSource(time.Now().UnixNano())
				rander := rand.New(randSource)
				rander.Shuffle(len(options), func(i, j int) {
					options[i], options[j] = options[j], options[i]
				})

				var keyboard [][]InlineKeyboardButton
				for _, opt := range options {
					keyboard = append(keyboard, []InlineKeyboardButton{{Text: opt.Text, CallbackData: opt.Data}})
				}
				replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
			}

			payload := map[string]interface{}{
				"chat_id":    update.CallbackQuery.Message.Chat.ID,
				"text":       responseText,
				"parse_mode": "Markdown",
			}
			if replyMarkup != nil {
				payload["reply_markup"] = replyMarkup
			}
			
			replyBody, _ := json.Marshal(payload)
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))

		// EVENT 2: User clicked an answer
		} else if dataParts[0] == "ans" && len(dataParts) == 4 {
			status := dataParts[1]
			questionID := dataParts[2]
			category := dataParts[3]

			var explanation string
			db.QueryRow("SELECT explanation FROM questions WHERE id = $1", questionID).Scan(&explanation)

			var responseText string
			var nextKeyboard InlineKeyboardMarkup

			// Split the logic for correct vs incorrect answers
			if status == "correct" {
				responseText = "✅ *Correct!*\n\n*Explanation:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
						{{Text: "Change Topic 🔄", CallbackData: "cmd:categories"}},
					},
				}
			} else {
				responseText = "❌ *Incorrect!*\n\n*Explanation:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						// Give them a button to retry this specific question
						{{Text: "Try Again 🔄", CallbackData: fmt.Sprintf("retry:%s:%s", questionID, category)}},
						{{Text: "Change Topic 🔄", CallbackData: "cmd:categories"}},
					},
				}
			}

			replyBody, _ := json.Marshal(map[string]interface{}{
				"chat_id":      update.CallbackQuery.Message.Chat.ID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))
			
		// EVENT 3: User clicked "Try Again"
		} else if dataParts[0] == "retry" && len(dataParts) == 3 {
			questionID := dataParts[1]
			category := dataParts[2]

			// Fetch the EXACT same question using the ID
			var q TKTQuestion
			err := db.QueryRow(`
				SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2, explanation 
				FROM questions 
				WHERE id = $1`, questionID).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2, &q.Explanation)

			var responseText string
			var replyMarkup interface{} = nil

			if err != nil {
				responseText = "Error fetching question. Let's try a new one."
				replyMarkup = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
					},
				}
			} else {
				responseText = fmt.Sprintf("📚 *Topic: %s*\n\n%s", category, q.QuestionText)

				type Option struct {
					Text string
					Data string
				}
				
				options := []Option{
					{Text: q.CorrectOption, Data: fmt.Sprintf("ans:correct:%d:%s", q.ID, category)},
					{Text: q.WrongOption1, Data: fmt.Sprintf("ans:wrong:%d:%s", q.ID, category)},
					{Text: q.WrongOption2, Data: fmt.Sprintf("ans:wrong:%d:%s", q.ID, category)},
				}

				randSource := rand.NewSource(time.Now().UnixNano())
				rander := rand.New(randSource)
				rander.Shuffle(len(options), func(i, j int) {
					options[i], options[j] = options[j], options[i]
				})

				var keyboard [][]InlineKeyboardButton
				for _, opt := range options {
					keyboard = append(keyboard, []InlineKeyboardButton{{Text: opt.Text, CallbackData: opt.Data}})
				}
				replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
			}

			payload := map[string]interface{}{
				"chat_id":    update.CallbackQuery.Message.Chat.ID,
				"text":       responseText,
				"parse_mode": "Markdown",
			}
			if replyMarkup != nil {
				payload["reply_markup"] = replyMarkup
			}
			
			replyBody, _ := json.Marshal(payload)
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))

		// EVENT 4: User clicked "Change Topic"
		} else if dataParts[0] == "cmd" && dataParts[1] == "categories" {
			update.Message = update.CallbackQuery.Message
			update.Message.Text = "/test"
		}
	} 
	
	// ==========================================
	// SCENARIO B: THE USER TYPED A MESSAGE
	// ==========================================
	if update.Message != nil {
		responseText := "I received your message! Type /test to practice."
		var replyMarkup interface{} = nil

		if update.Message.Text == "/start" {
			responseText = "Welcome to the TKT Prep Bot! 🚀\n\nType /test to start practicing."
		} else if update.Message.Text == "/test" {
			
			rows, err := db.Query("SELECT DISTINCT category FROM questions WHERE category IS NOT NULL AND category != ''")
			
			if err != nil {
				responseText = "Error fetching categories: " + err.Error()
			} else {
				defer rows.Close()
				responseText = "📂 *Choose a topic to practice:*"
				var keyboard [][]InlineKeyboardButton
				
				for rows.Next() {
					var cat string
					if err := rows.Scan(&cat); err == nil {
						keyboard = append(keyboard, []InlineKeyboardButton{{Text: cat, CallbackData: "cat:" + cat}})
					}
				}
				
				if len(keyboard) > 0 {
					replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
				} else {
					responseText = "No categories found. Please add some in your Supabase table!"
				}
			}
		}

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