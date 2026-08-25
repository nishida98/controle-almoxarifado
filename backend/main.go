package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type App struct {
	mu         sync.Mutex
	sessions   map[string]string
	collection *mongo.Collection
	user       string
	password   string
}

type Record struct {
	ID         string     `json:"id" bson:"_id"`
	PersonName string     `json:"personName" bson:"personName"`
	ItemName   string     `json:"itemName" bson:"itemName"`
	Quantity   int        `json:"quantity" bson:"quantity"`
	Notes      string     `json:"notes" bson:"notes"`
	Status     string     `json:"status" bson:"status"`
	CheckoutAt time.Time  `json:"checkoutAt" bson:"checkoutAt"`
	ReturnedAt *time.Time `json:"returnedAt,omitempty" bson:"returnedAt,omitempty"`
	CreatedBy  string     `json:"createdBy" bson:"createdBy"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
	User  string `json:"user"`
}

type CreateRecordRequest struct {
	PersonName string `json:"personName"`
	ItemName   string `json:"itemName"`
	Quantity   int    `json:"quantity"`
	Notes      string `json:"notes"`
}

type DailyReport struct {
	Date     string         `json:"date"`
	ByPerson []ReportBucket `json:"byPerson"`
	ByItem   []ReportBucket `json:"byItem"`
	Records  []Record       `json:"records"`
	Summary  ReportSummary  `json:"summary"`
}

type ReportBucket struct {
	Name     string `json:"name"`
	Total    int    `json:"total"`
	Pending  int    `json:"pending"`
	Returned int    `json:"returned"`
}

type ReportSummary struct {
	TotalRecords int `json:"totalRecords"`
	Pending      int `json:"pending"`
	Returned     int `json:"returned"`
	TotalItems   int `json:"totalItems"`
}

func main() {
	loadEnvFile(".env")

	client, collection, err := connectMongo(context.Background())
	if err != nil {
		log.Fatalf("connect mongodb: %v", err)
	}
	defer func() {
		if err := client.Disconnect(context.Background()); err != nil {
			log.Printf("disconnect mongodb: %v", err)
		}
	}()

	app := &App{
		sessions:   map[string]string{},
		collection: collection,
		user:       env("APP_USER", "admin"),
		password:   env("APP_PASSWORD", "admin123"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", app.handleLogin)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /api/records", app.auth(http.HandlerFunc(app.handleListRecords)))
	mux.Handle("POST /api/records", app.auth(http.HandlerFunc(app.handleCreateRecord)))
	mux.Handle("PATCH /api/records/{id}/return", app.auth(http.HandlerFunc(app.handleReturnRecord)))
	mux.Handle("GET /api/reports/daily", app.auth(http.HandlerFunc(app.handleDailyReport)))

	addr := env("APP_ADDR", ":8080")
	log.Printf("backend listening on %s", addr)
	if err := http.ListenAndServe(addr, cors(mux)); err != nil {
		log.Fatal(err)
	}
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "JSON invalido")
		return
	}
	if req.Username != a.user || req.Password != a.password {
		writeError(w, http.StatusUnauthorized, "Usuario ou senha invalidos")
		return
	}

	token, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao criar sessao")
		return
	}

	a.mu.Lock()
	a.sessions[token] = req.Username
	a.mu.Unlock()

	writeJSON(w, http.StatusOK, LoginResponse{Token: token, User: req.Username})
}

func (a *App) handleListRecords(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")

	filter, err := dateFilter(date)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Data invalida")
		return
	}
	records, err := a.findRecords(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao buscar registros")
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (a *App) handleCreateRecord(w http.ResponseWriter, r *http.Request) {
	var req CreateRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "JSON invalido")
		return
	}
	req.PersonName = strings.TrimSpace(req.PersonName)
	req.ItemName = strings.TrimSpace(req.ItemName)
	req.Notes = strings.TrimSpace(req.Notes)
	if req.PersonName == "" || req.ItemName == "" {
		writeError(w, http.StatusBadRequest, "Pessoa e item sao obrigatorios")
		return
	}
	if req.Quantity <= 0 {
		req.Quantity = 1
	}

	user := r.Context().Value(userContextKey{}).(string)

	id, err := newToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao criar registro")
		return
	}
	record := Record{
		ID:         id,
		PersonName: req.PersonName,
		ItemName:   req.ItemName,
		Quantity:   req.Quantity,
		Notes:      req.Notes,
		Status:     "pending",
		CheckoutAt: time.Now(),
		CreatedBy:  user,
	}
	if _, err := a.collection.InsertOne(r.Context(), record); err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao salvar registro")
		return
	}

	writeJSON(w, http.StatusCreated, record)
}

func (a *App) handleReturnRecord(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "ID obrigatorio")
		return
	}

	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"status":     "returned",
			"returnedAt": now,
		},
	}
	result, err := a.collection.UpdateOne(r.Context(), bson.M{"_id": id}, update)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao salvar devolucao")
		return
	}
	if result.MatchedCount == 0 {
		writeError(w, http.StatusNotFound, "Registro nao encontrado")
		return
	}

	var record Record
	if err := a.collection.FindOne(r.Context(), bson.M{"_id": id}).Decode(&record); err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao buscar registro")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (a *App) handleDailyReport(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}

	filter, err := dateFilter(date)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Data invalida")
		return
	}
	records, err := a.findRecords(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao buscar relatorio")
		return
	}

	byPerson := map[string]*ReportBucket{}
	byItem := map[string]*ReportBucket{}
	summary := ReportSummary{}

	for _, record := range records {
		summary.TotalRecords++
		summary.TotalItems += record.Quantity
		if record.Status == "returned" {
			summary.Returned++
		} else {
			summary.Pending++
		}
		addBucket(byPerson, record.PersonName, record)
		addBucket(byItem, record.ItemName, record)
	}

	writeJSON(w, http.StatusOK, DailyReport{
		Date:     date,
		ByPerson: buckets(byPerson),
		ByItem:   buckets(byItem),
		Records:  records,
		Summary:  summary,
	})
}

func addBucket(index map[string]*ReportBucket, name string, record Record) {
	bucket, ok := index[name]
	if !ok {
		bucket = &ReportBucket{Name: name}
		index[name] = bucket
	}
	bucket.Total += record.Quantity
	if record.Status == "returned" {
		bucket.Returned += record.Quantity
	} else {
		bucket.Pending += record.Quantity
	}
}

func buckets(index map[string]*ReportBucket) []ReportBucket {
	result := make([]ReportBucket, 0, len(index))
	for _, bucket := range index {
		result = append(result, *bucket)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result
}

type userContextKey struct{}

func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token := strings.TrimPrefix(header, "Bearer ")
		if token == header || token == "" {
			writeError(w, http.StatusUnauthorized, "Sessao obrigatoria")
			return
		}

		a.mu.Lock()
		user, ok := a.sessions[token]
		a.mu.Unlock()
		if !ok {
			writeError(w, http.StatusUnauthorized, "Sessao invalida")
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func newToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func loadEnvFile(path string) {
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}

func connectMongo(ctx context.Context) (*mongo.Client, *mongo.Collection, error) {
	uri := env("MONGODB_URI", "")
	if uri == "" {
		return nil, nil, errors.New("MONGODB_URI nao configurada")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, nil, err
	}

	database := env("MONGODB_DATABASE", "controle_almoxarifado")
	collection := env("MONGODB_COLLECTION", "records")
	return client, client.Database(database).Collection(collection), nil
}

func (a *App) findRecords(ctx context.Context, filter bson.M) ([]Record, error) {
	opts := options.Find().SetSort(bson.D{{Key: "checkoutAt", Value: -1}})
	cursor, err := a.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var records []Record
	if err := cursor.All(ctx, &records); err != nil {
		return nil, err
	}
	if records == nil {
		records = []Record{}
	}
	return records, nil
}

func dateFilter(date string) (bson.M, error) {
	if strings.TrimSpace(date) == "" {
		return bson.M{}, nil
	}
	start, err := time.ParseInLocation("2006-01-02", date, time.Local)
	if err != nil {
		return nil, err
	}
	end := start.AddDate(0, 0, 1)
	return bson.M{
		"checkoutAt": bson.M{
			"$gte": start,
			"$lt":  end,
		},
	}, nil
}
