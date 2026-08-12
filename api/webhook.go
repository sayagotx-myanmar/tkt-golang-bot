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
	"strconv"
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
	MessageID int      `json:"message_id"`
	Chat      *Chat    `json:"chat"`
	Text      string   `json:"text"`
	Contact   *Contact `json:"contact"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	UserID      int64  `json:"user_id"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type ReplyKeyboardMarkup struct {
	Keyboard        [][]KeyboardButton `json:"keyboard"`
	ResizeKeyboard  bool               `json:"resize_keyboard"`
	OneTimeKeyboard bool               `json:"one_time_keyboard"`
}

type KeyboardButton struct {
	Text           string `json:"text"`
	RequestContact bool   `json:"request_contact"`
}

type ReplyKeyboardRemove struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
}

// ==========================================
// 2. DATABASE & GLOBALS
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
// 3. AUTH & TRIAL LOGIC (THE GATEKEEPER)
// ==========================================

// checkUserAccess verifies if a user is registered, authorized, and hasn't expired.
func checkUserAccess(userID int64) (bool, string) {
	var isAuth bool
	var expiresAt time.Time

	err := db.QueryRow("SELECT is_authorized, expires_at FROM users WHERE telegram_id = $1", userID).Scan(&isAuth, &expiresAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return false, "unregistered" // Special flag to prompt for phone number
		}
		log.Printf("Database error checking auth: %v", err)
		return false, "⚠️ A database error occurred. Please try again later."
	}

	if !isAuth {
		return false, "❌ You are not authorized to use this bot. Please contact @SayaGotX."
	}

	// Check if trial/subscription has expired
	if time.Now().After(expiresAt) {
		// Auto-revoke access in the database to prevent further checks
		db.Exec("UPDATE users SET subscription_status = 'expired', is_authorized = false WHERE telegram_id = $1", userID)
		return false, "⏳ *Your trial has expired!*\n\nPlease contact @SayaGotX or enter an activation code to renew your subscription."
	}

	return true, "" // User is good to go!
}

// ==========================================
// 4. TELEGRAM API HELPERS
// ==========================================

func sendTelegramRequest(endpoint string, payload interface{}) {
	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", botToken, endpoint)

	body, _ := json.Marshal(payload)
	http.Post(apiURL, "application/json", bytes.NewBuffer(body))
}

func getMainMenu() ReplyKeyboardMarkup {
	return ReplyKeyboardMarkup{
		Keyboard: [][]KeyboardButton{
			{{Text: "📚 Practice Modules"}, {Text: "📊 My Progress"}},
		},
		ResizeKeyboard: true,
	}
}

func getAuthKeyboard() ReplyKeyboardMarkup {
	return ReplyKeyboardMarkup{
		Keyboard: [][]KeyboardButton{
			{{Text: "📱 Share My Phone Number", RequestContact: true}},
		},
		ResizeKeyboard:  true,
		OneTimeKeyboard: true,
	}
}

// ==========================================
// 5. MAIN WEBHOOK HANDLER
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

	// Route the update to the appropriate handler
	if update.CallbackQuery != nil {
		handleCallbackQuery(update.CallbackQuery)
	} else if update.Message != nil {
		handleMessage(update.Message)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

// ==========================================
// 6. SCENARIO A: CALLBACK QUERIES
// ==========================================

func handleCallbackQuery(cb *CallbackQuery) {
	// 1. Acknowledge the click immediately to stop the loading spinner
	sendTelegramRequest("answerCallbackQuery", map[string]string{
		"callback_query_id": cb.ID,
	})

	userID := cb.Message.Chat.ID
	messageID := cb.Message.MessageID

	// 2. Gatekeeper: Verify access and expiration before processing clicks
	isAllowed, denyMessage := checkUserAccess(userID)
	if !isAllowed {
		if denyMessage == "unregistered" {
			denyMessage = "🔒 *Access Restricted*\nPlease type /start to verify your account."
		}
		sendTelegramRequest("sendMessage", map[string]interface{}{
			"chat_id":    userID,
			"text":       denyMessage,
			"parse_mode": "Markdown",
		})
		return
	}

	dataParts := strings.Split(cb.Data, ":")
	action := dataParts[0]

	// 3. Route to specific logic based on callback data
	switch action {
	case "cat":
		if len(dataParts) == 2 {
			sendQuestion(userID, messageID, dataParts[1])
		}
	case "ans":
		if len(dataParts) == 4 {
			handleAnswer(userID, messageID, dataParts[1], dataParts[2], dataParts[3], true)
		}
	case "retryans":
		if len(dataParts) == 4 {
			handleAnswer(userID, messageID, dataParts[1], dataParts[2], dataParts[3], false)
		}
	case "retry":
		if len(dataParts) == 3 {
			showRetryQuestion(userID, messageID, dataParts[1], dataParts[2])
		}
	case "reset":
		if len(dataParts) == 2 {
			resetCategory(userID, messageID, dataParts[1])
		}
	}
}

// ==========================================
// 7. SCENARIO B: MESSAGES & CONTACTS
// ==========================================

func handleMessage(msg *Message) {
	userID := msg.Chat.ID
	text := msg.Text

	// EVENT: User shared their contact info
	if msg.Contact != nil {
		processContactShare(userID, msg.Contact.PhoneNumber)
		return
	}

	// NEW: Intercept /redeem command BEFORE the gatekeeper blocks them
	if strings.HasPrefix(text, "/redeem ") {
		code := strings.TrimSpace(strings.TrimPrefix(text, "/redeem "))
		if code == "" {
			sendTelegramRequest("sendMessage", map[string]interface{}{
				"chat_id":    userID,
				"text":       "Please provide a code. Example: `/redeem TKT-AUG-1234`",
				"parse_mode": "Markdown",
			})
			return
		}
		processRedemption(userID, code)
		return
	}

	// Gatekeeper: Check status for normal text commands
	isAllowed, denyMessage := checkUserAccess(userID)

	if text == "/start" {
		if isAllowed {
			sendTelegramRequest("sendMessage", map[string]interface{}{
				"chat_id":      userID,
				"text":         "Welcome back! 🚀\n\nUse the menu below to start practicing or check your scores.",
				"reply_markup": getMainMenu(),
			})
		} else {
			sendTelegramRequest("sendMessage", map[string]interface{}{
				"chat_id":      userID,
				"text":         "🔒 *Access Restricted*\n\nTo access the TKT Practice materials, you must verify your purchase.\n\nPlease tap the button below to share your phone number securely.",
				"parse_mode":   "Markdown",
				"reply_markup": getAuthKeyboard(),
			})
		}
		return
	}

	// Block unauthorized users from proceeding further
	if !isAllowed {
		if denyMessage == "unregistered" {
			denyMessage = "❌ You must be authorized to use this bot. Type /start to verify your account."
		}
		sendTelegramRequest("sendMessage", map[string]interface{}{
			"chat_id":    userID,
			"text":       denyMessage,
			"parse_mode": "Markdown",
		})
		return
	}

	// Handle standard text commands
	switch text {
	case "📚 Practice Modules", "/test":
		showCategories(userID)
	case "📊 My Progress", "/progress":
		showProgress(userID)
	default:
		sendTelegramRequest("sendMessage", map[string]interface{}{
			"chat_id":      userID,
			"text":         "I didn't understand that command. Please use the menu below.",
			"reply_markup": getMainMenu(),
		})
	}
}

// ==========================================
// 8. BUSINESS LOGIC HELPER FUNCTIONS
// ==========================================

func processContactShare(userID int64, phone string) {
	// Clean the phone number
	phone = strings.ReplaceAll(phone, "+", "")
	phone = strings.TrimPrefix(phone, "95")
	if strings.HasPrefix(phone, "09") {
		phone = strings.TrimPrefix(phone, "0")
	}

	var dbID int
	// Check if phone exists in whitelist (authorized_users)
	err := db.QueryRow("SELECT id FROM authorized_users WHERE phone_number = $1", phone).Scan(&dbID)

	if err == nil {
		// Update authorized_users table
		db.Exec("UPDATE authorized_users SET telegram_user_id = $1 WHERE phone_number = $2", userID, phone)

		// Get Trial Months from Env Var (Default to 1)
		trialMonths := 1
		if val := os.Getenv("TRIAL_MONTHS"); val != "" {
			if parsed, err := strconv.Atoi(val); err == nil {
				trialMonths = parsed
			}
		}
		expiresAt := time.Now().AddDate(0, trialMonths, 0)

		// Create/Update the active user in the 'users' table to start their trial
		db.Exec(`
            INSERT INTO users (telegram_id, is_authorized, subscription_status, joined_at, expires_at) 
            VALUES ($1, true, 'trial', NOW(), $2)
            ON CONFLICT (telegram_id) DO UPDATE SET 
                is_authorized = true, 
                subscription_status = 'trial', 
                expires_at = $2`,
			userID, expiresAt)

		// Send success message and Main Menu
		sendTelegramRequest("sendMessage", map[string]interface{}{
			"chat_id":      userID,
			"text":         fmt.Sprintf("✅ *Verification successful!*\n\nYour account has been linked. Your trial is active until *%s*.", expiresAt.Format("Jan 02, 2006")),
			"parse_mode":   "Markdown",
			"reply_markup": getMainMenu(),
		})

		// Send the Discussion Group Invite
		groupKeyboard := InlineKeyboardMarkup{
			InlineKeyboard: [][]InlineKeyboardButton{
				{{
					Text: "💬 Join Discussion Group",
					URL:  "https://t.me/+gdgq6rlcuS43OTA1",
				}},
			},
		}

		sendTelegramRequest("sendMessage", map[string]interface{}{
			"chat_id":      userID,
			"text":         "🎉 *Welcome to the TKT Prep Community!*\n\nPlease join our exclusive Telegram group to discuss questions, share insights, and connect with other teachers.",
			"parse_mode":   "Markdown",
			"reply_markup": groupKeyboard,
		})

	} else {
		sendTelegramRequest("sendMessage", map[string]interface{}{
			"chat_id":      userID,
			"text":         "❌ Sorry, this phone number has not been granted access yet. Please contact the administrator after purchasing.",
			"reply_markup": ReplyKeyboardRemove{RemoveKeyboard: true},
		})
	}
}

func showCategories(userID int64) {
	rows, err := db.Query("SELECT DISTINCT category FROM questions WHERE category IS NOT NULL AND category != ''")
	if err != nil {
		sendTelegramRequest("sendMessage", map[string]interface{}{"chat_id": userID, "text": "Error fetching categories."})
		return
	}
	defer rows.Close()

	var keyboard [][]InlineKeyboardButton
	for rows.Next() {
		var cat string
		if err := rows.Scan(&cat); err == nil {
			keyboard = append(keyboard, []InlineKeyboardButton{{Text: "📘 " + cat, CallbackData: "cat:" + cat}})
		}
	}

	if len(keyboard) > 0 {
		sendTelegramRequest("sendMessage", map[string]interface{}{
			"chat_id":      userID,
			"text":         "📂 *Choose a module to practice:*",
			"parse_mode":   "Markdown",
			"reply_markup": InlineKeyboardMarkup{InlineKeyboard: keyboard},
		})
	} else {
		sendTelegramRequest("sendMessage", map[string]interface{}{"chat_id": userID, "text": "No modules found."})
	}
}

func showProgress(userID int64) {
	rows, err := db.Query("SELECT category, points, attempts FROM user_progress WHERE telegram_user_id = $1 ORDER BY category", userID)
	if err != nil {
		sendTelegramRequest("sendMessage", map[string]interface{}{"chat_id": userID, "text": "Error fetching progress."})
		return
	}
	defer rows.Close()

	responseText := "📊 *Your Learning Progress:*\n\n"
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
			keyboard = append(keyboard, []InlineKeyboardButton{{Text: fmt.Sprintf("🔄 Reset %s", cat), CallbackData: "reset:" + cat}})
		}
	}

	var replyMarkup interface{} = nil
	if !hasData {
		responseText = "📊 *Your Learning Progress:*\n\nYou haven't completed any practice questions yet! Tap 'Practice Modules' to begin."
	} else {
		responseText += "\n_Need a fresh start? Use the buttons below to reset a module's progress._"
		replyMarkup = InlineKeyboardMarkup{InlineKeyboard: keyboard}
	}

	payload := map[string]interface{}{
		"chat_id":    userID,
		"text":       responseText,
		"parse_mode": "Markdown",
	}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}
	sendTelegramRequest("sendMessage", payload)
}

func sendQuestion(userID int64, messageID int, category string) {
	var q TKTQuestion
	err := db.QueryRow(`
        SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2, explanation 
        FROM questions 
        WHERE category = $1 
        AND id NOT IN (SELECT question_id FROM answered_questions WHERE telegram_user_id = $2)
        ORDER BY RANDOM() 
        LIMIT 1`, category, userID).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2, &q.Explanation)

	if err == sql.ErrNoRows {
		sendTelegramRequest("editMessageText", map[string]interface{}{
			"chat_id":    userID,
			"message_id": messageID,
			"text":       fmt.Sprintf("🎉 *Congratulations!*\n\nYou have completed all the practice questions for *%s*.\n\nCheck your progress from the main menu.", category),
			"parse_mode": "Markdown",
		})
		return
	}

	options := []struct{ Text, Status string }{
		{q.CorrectOption, "correct"}, {q.WrongOption1, "wrong"}, {q.WrongOption2, "wrong"},
	}

	rander := rand.New(rand.NewSource(time.Now().UnixNano()))
	rander.Shuffle(len(options), func(i, j int) { options[i], options[j] = options[j], options[i] })

	responseText := fmt.Sprintf("📚 *Topic: %s*\n\n%s\n\n*A)* %s\n*B)* %s\n*C)* %s", category, q.QuestionText, options[0].Text, options[1].Text, options[2].Text)

	keyboard := InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{{
			{Text: "A", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[0].Status, q.ID, category)},
			{Text: "B", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[1].Status, q.ID, category)},
			{Text: "C", CallbackData: fmt.Sprintf("ans:%s:%d:%s", options[2].Status, q.ID, category)},
		}},
	}

	sendTelegramRequest("editMessageText", map[string]interface{}{
		"chat_id":      userID,
		"message_id":   messageID,
		"text":         responseText,
		"parse_mode":   "Markdown",
		"reply_markup": keyboard,
	})
}

func handleAnswer(userID int64, messageID int, status, questionID, category string, isFirstTry bool) {
	if isFirstTry {
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
                attempts = user_progress.attempts + EXCLUDED.attempts`, userID, category, pointsToAdd)

		db.Exec("INSERT INTO answered_questions (telegram_user_id, question_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", userID, questionID)
	}

	var explanation, qText string
	db.QueryRow("SELECT question_text, explanation FROM questions WHERE id = $1", questionID).Scan(&qText, &explanation)

	var responseText string
	var nextKeyboard InlineKeyboardMarkup

	if status == "correct" {
		responseText = fmt.Sprintf("✅ *Correct!*\n\n_%s_\n\n*💡 Explanation:*\n%s", qText, explanation)
		nextKeyboard = InlineKeyboardMarkup{
			InlineKeyboard: [][]InlineKeyboardButton{{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}}},
		}
	} else {
		responseText = fmt.Sprintf("❌ *Incorrect!*\n\n_%s_\n\n*💡 Hint:*\n%s", qText, explanation)
		if isFirstTry {
			nextKeyboard = InlineKeyboardMarkup{
				InlineKeyboard: [][]InlineKeyboardButton{
					{{Text: "Try Again 🔄", CallbackData: fmt.Sprintf("retry:%s:%s", questionID, category)}},
					{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}},
				},
			}
		} else {
			nextKeyboard = InlineKeyboardMarkup{
				InlineKeyboard: [][]InlineKeyboardButton{{{Text: "Next Question ➡️", CallbackData: fmt.Sprintf("cat:%s", category)}}},
			}
		}
	}

	sendTelegramRequest("editMessageText", map[string]interface{}{
		"chat_id":      userID,
		"message_id":   messageID,
		"text":         responseText,
		"parse_mode":   "Markdown",
		"reply_markup": nextKeyboard,
	})
}

func showRetryQuestion(userID int64, messageID int, questionID, category string) {
	var q TKTQuestion
	db.QueryRow("SELECT id, question_text, correct_option, wrong_option_1, wrong_option_2 FROM questions WHERE id = $1", questionID).Scan(&q.ID, &q.QuestionText, &q.CorrectOption, &q.WrongOption1, &q.WrongOption2)

	options := []struct{ Text, Status string }{
		{q.CorrectOption, "correct"}, {q.WrongOption1, "wrong"}, {q.WrongOption2, "wrong"},
	}

	rander := rand.New(rand.NewSource(time.Now().UnixNano()))
	rander.Shuffle(len(options), func(i, j int) { options[i], options[j] = options[j], options[i] })

	responseText := fmt.Sprintf("🔄 *Try Again!*\n\n📚 *Topic: %s*\n\n%s\n\n*A)* %s\n*B)* %s\n*C)* %s", category, q.QuestionText, options[0].Text, options[1].Text, options[2].Text)

	keyboard := InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{{
			{Text: "A", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[0].Status, q.ID, category)},
			{Text: "B", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[1].Status, q.ID, category)},
			{Text: "C", CallbackData: fmt.Sprintf("retryans:%s:%d:%s", options[2].Status, q.ID, category)},
		}},
	}

	sendTelegramRequest("editMessageText", map[string]interface{}{
		"chat_id":      userID,
		"message_id":   messageID,
		"text":         responseText,
		"parse_mode":   "Markdown",
		"reply_markup": keyboard,
	})
}

func resetCategory(userID int64, messageID int, category string) {
	db.Exec("UPDATE user_progress SET points = 0, attempts = 0 WHERE telegram_user_id = $1 AND category = $2", userID, category)
	db.Exec("DELETE FROM answered_questions WHERE telegram_user_id = $1 AND question_id IN (SELECT id FROM questions WHERE category = $2)", userID, category)

	sendTelegramRequest("editMessageText", map[string]interface{}{
		"chat_id":    userID,
		"message_id": messageID,
		"text":       fmt.Sprintf("🔄 *%s* has been reset. You can now practice it from the beginning.", category),
		"parse_mode": "Markdown",
	})
}

// NEW: Redemption logic for activation codes
func processRedemption(userID int64, code string) {
	// 1. Check if the code exists and is NOT used
	var codeID int
	err := db.QueryRow("SELECT id FROM activation_codes WHERE code = $1 AND is_used = false", code).Scan(&codeID)

	if err != nil {
		if err == sql.ErrNoRows {
			sendTelegramRequest("sendMessage", map[string]interface{}{
				"chat_id": userID,
				"text":    "❌ Invalid or already used activation code. Please check your code and try again.",
			})
		} else {
			sendTelegramRequest("sendMessage", map[string]interface{}{
				"chat_id": userID,
				"text":    "⚠️ A database error occurred. Please try again later.",
			})
		}
		return
	}

	// 2. Mark the code as used and link it to the user who redeemed it
	db.Exec("UPDATE activation_codes SET is_used = true, telegram_id = $1 WHERE id = $2", userID, codeID)

	// 3. Extend the user's expiration date by 1 month
	_, err = db.Exec(`
		UPDATE users 
		SET 
			expires_at = GREATEST(NOW(), expires_at) + INTERVAL '1 month',
			is_authorized = true,
			subscription_status = 'active'
		WHERE telegram_id = $1
	`, userID)

	if err != nil {
		log.Printf("Error updating expiration: %v", err)
		return
	}

	// 4. Fetch the new expiration date to show the user
	var newExpiry time.Time
	db.QueryRow("SELECT expires_at FROM users WHERE telegram_id = $1", userID).Scan(&newExpiry)

	// 5. Send a success message
	sendTelegramRequest("sendMessage", map[string]interface{}{
		"chat_id":      userID,
		"text":         fmt.Sprintf("🎉 *Subscription Renewed!*\n\nYour code was accepted. Your access has been extended until *%s*.", newExpiry.Format("Jan 02, 2006")),
		"parse_mode":   "Markdown",
		"reply_markup": getMainMenu(),
	})
}