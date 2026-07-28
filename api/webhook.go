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
				type Option struct {
					Text   string
					Status string
				}
				
				options := []Option{
					{Text: q.CorrectOption, Status: "correct"},
					{Text: q.WrongOption1, Status: "wrong"},
					{Text: q.WrongOption2, Status: "wrong"},
				}

				randSource := rand.NewSource(time.Now().UnixNano())
				rander := rand.New(randSource)
				rander.Shuffle(len(options), func(i, j int) {
					options[i], options[j] = options[j], options[i]
				})

				responseText = fmt.Sprintf("📚 *Topic: %s*\n\n%s\n\n*A)* %s\n*B)* %s\n*C)* %s", 
					category, q.QuestionText, options[0].Text, options[1].Text, options[2].Text)

				// Notice we use "ans" prefix for first-try answers
				keyboard := [][]InlineKeyboardButton{
					{
						{Text: "A", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[0].Status, q.ID, category)},
						{Text: "B", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[1].Status, q.ID, category)},
						{Text: "C", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[2].Status, q.ID, category)},
					},
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

		// EVENT 2: User clicked a FIRST-TRY answer
		} else if dataParts[0] == "ans" && len(dataParts) == 4 {
			status := dataParts[1]
			questionID := dataParts[2]
			category := dataParts[3]
			userID := update.CallbackQuery.Message.Chat.ID

			// Update database score (Upsert logic)
			pointsToAdd := 0
			if status == "correct" {
				pointsToAdd = 1
			}
			db.Exec(`
				INSERT INTO user_progress (telegram_user_id, category, points, attempts)
				VALUES ($1, $2, $3, 1)
				ON CONFLICT (telegram_user_id, category) 
				DO UPDATE SET 
					points = user_progress.points + EXCLUDED.points,
					attempts = user_progress.attempts + EXCLUDED.attempts`,
				userID, category, pointsToAdd)

			var explanation string
			db.QueryRow("SELECT explanation FROM questions WHERE id = $1", questionID).Scan(&explanation)

			var responseText string
			var nextKeyboard InlineKeyboardMarkup

			if status == "correct" {
				responseText = "✅ *Correct!*\n\n*Explanation:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
						{{Text: "Practice different module 🔄", CallbackData: "cmd:categories"}},
					},
				}
			} else {
				responseText = "❌ *Incorrect!*\n\n*Explanation:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Try Again 🔄", CallbackData: fmt.Sprintf("retry:%s:%s", questionID, category)}},
						{{Text: "Practice different module 🔄", CallbackData: "cmd:categories"}},
					},
				}
			}

			replyBody, _ := json.Marshal(map[string]interface{}{
				"chat_id":      userID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))

		// EVENT 3: User clicked an answer AFTER hitting Try Again (Does NOT count toward score/attempts)
		} else if dataParts[0] == "retryans" && len(dataParts) == 4 {
			status := dataParts[1]
			questionID := dataParts[2]
			category := dataParts[3]
			userID := update.CallbackQuery.Message.Chat.ID

			var explanation string
			db.QueryRow("SELECT explanation FROM questions WHERE id = $1", questionID).Scan(&explanation)

			var responseText string
			var nextKeyboard InlineKeyboardMarkup

			if status == "correct" {
				responseText = "✅ *Correct!*\n\n*Explanation:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
						{{Text: "Practice different module 🔄", CallbackData: "cmd:categories"}},
					},
				}
			} else {
				responseText = "❌ *Incorrect!*\n\n*Explanation:*\n" + explanation
				nextKeyboard = InlineKeyboardMarkup{
					InlineKeyboard: [][]InlineKeyboardButton{
						{{Text: "Try Again 🔄", CallbackData: fmt.Sprintf("retry:%s:%s", questionID, category)}},
						{{Text: "Practice different module 🔄", CallbackData: "cmd:categories"}},
					},
				}
			}

			replyBody, _ := json.Marshal(map[string]interface{}{
				"chat_id":      userID,
				"text":         responseText,
				"parse_mode":   "Markdown",
				"reply_markup": nextKeyboard,
			})
			http.Post(apiURL+"sendMessage", "application/json", bytes.NewBuffer(replyBody))
			
		// EVENT 4: User clicked "Try Again" (Uses retryans prefix so score isn't duplicated)
		} else if dataParts[0] == "retry" && len(dataParts) == 3 {
			questionID := dataParts[1]
			category := dataParts[2]

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
				type Option struct {
					Text   string
					Status string
				}
				
				options := []Option{
					{Text: q.CorrectOption, Status: "correct"},
					{Text: q.WrongOption1, Status: "wrong"},
					{Text: q.WrongOption2, Status: "wrong"},
				}

				randSource := rand.NewSource(time.Now().UnixNano())
				rander := rand.New(randSource)
				rander.Shuffle(len(options), func(i, j int) {
					options[i], options[j] = options[j], options[i]
				})

				responseText = fmt.Sprintf("📚 *Topic: %s*\n\n%s\n\n*A)* %s\n*B)* %s\n*C)* %s", 
					category, q.QuestionText, options[0].Text, options[1].Text, options[2].Text)

				keyboard := [][]InlineKeyboardButton{
					{
						{Text: "A", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[0].Status, q.ID, category)},
						{Text: "B", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[1].Status, q.ID, category)},
						{Text: "C", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[2].Status, q.ID, category)},
					},
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

		// EVENT 5: User clicked "Practice different module"
		} else if dataParts[0] == "cmd" && dataParts[1] == "categories" {
			update.Message = update.CallbackQuery.Message
			update.Message.Text = "/test"

		// EVENT 6: User clicked "Reset Module"
		} else if dataParts[0] == "reset" && len(dataParts) == 2 {
			categoryToReset := dataParts[1]
			userID := update.CallbackQuery.Message.Chat.ID

			db.Exec("UPDATE user_progress SET points = 0, attempts = 0 WHERE telegram_user_id = $1 AND category = $2", userID, categoryToReset)

			// Confirm reset and show progress again
			update.Message = update.CallbackQuery.Message
			update.Message.Text = "/progress"
		}
	} 
	
	// ==========================================
	// SCENARIO B: THE USER TYPED A MESSAGE
	// ==========================================
	if update.Message != nil {
		responseText := "I received your message! Type /test to practice."
		var replyMarkup interface{} = nil

		if update.Message.Text == "/start" {
			responseText = "Welcome to the TKT Prep Bot! 🚀\n\nType /test to start practicing.\nType /progress to check your scores."
		} else if update.Message.Text == "/test" {
			
			rows, err := db.Query("SELECT DISTINCT category FROM questions WHERE category IS NOT NULL AND category != ''")
			
			if err != nil {
				responseText = "Error fetching categories: " + err.Error()
			} else {
				defer rows.Close()
				responseText = "📂 *Choose a module to practice:*"
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
		} else if update.Message.Text == "/progress" {
			userID := update.Message.Chat.ID
			rows, err := db.Query("SELECT category, points, attempts FROM user_progress WHERE telegram_user_id = $1", userID)

			if err != nil {
				responseText = "Error fetching your progress."
			} else {
				defer rows.Close()
				responseText = "📊 *Your Learning Progress:*\n\n"
				var keyboard [][]InlineKeyboardButton
				hasData := false

				for rows.Next() {
					var cat string
					var pts, att int
					if err := rows.Scan(&cat, &pts, &att); err == nil {
						hasData = true
						percentage := 0
						if att > 0 {
							percentage = (pts * 100) / att
						}
						responseText += fmt.Sprintf("🔹 *%s*\nScore: %d / %d (%d%%)\n\n", cat, pts, att, percentage)
						
						// Add a reset button for each module
						keyboard = append(keyboard, []InlineKeyboardButton{{Text: fmt.Sprintf("🔄 Reset %s", cat), CallbackData: "reset:" + cat}})
					}
				}

				if !hasData {
					responseText = "📊 *Your Learning Progress:*\n\nYou haven't completed any practice questions yet! Type /test to begin."
				} else {
					replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
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