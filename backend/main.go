package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type App struct {
	db          *dynamodb.Client
	table       string
	user        string
	password    string
	tokenSecret []byte
}

type Record struct {
	ID           string     `json:"id" dynamodbav:"id"`
	PersonName   string     `json:"personName" dynamodbav:"personName"`
	ItemName     string     `json:"itemName" dynamodbav:"itemName"`
	Quantity     int        `json:"quantity" dynamodbav:"quantity"`
	Notes        string     `json:"notes" dynamodbav:"notes"`
	Status       string     `json:"status" dynamodbav:"status"`
	CheckoutAt   time.Time  `json:"checkoutAt" dynamodbav:"checkoutAt"`
	CheckoutDate string     `json:"-" dynamodbav:"checkoutDate"`
	ReturnedAt   *time.Time `json:"returnedAt,omitempty" dynamodbav:"returnedAt,omitempty"`
	CreatedBy    string     `json:"createdBy" dynamodbav:"createdBy"`
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

	app, err := newApp(context.Background())
	if err != nil {
		log.Fatalf("init backend: %v", err)
	}

	handler := cors(app.routes())
	if env("AWS_LAMBDA_FUNCTION_NAME", "") != "" || env("LAMBDA_TASK_ROOT", "") != "" {
		lambda.Start(httpadapter.New(handler).ProxyWithContext)
		return
	}

	addr := serverAddress()
	log.Printf("backend listening on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

func newApp(ctx context.Context) (*App, error) {
	client, err := connectDynamo(ctx)
	if err != nil {
		return nil, err
	}

	table := env("DYNAMODB_TABLE", "")
	if table == "" {
		return nil, errors.New("DYNAMODB_TABLE nao configurada")
	}

	password := env("APP_PASSWORD", "admin123")
	secret := env("APP_TOKEN_SECRET", password)
	if len(secret) < 16 {
		log.Print("APP_TOKEN_SECRET nao configurado ou curto; usando APP_PASSWORD como segredo do token")
	}

	log.Printf("dynamodb table=%s", table)
	return &App{
		db:          client,
		table:       table,
		user:        env("APP_USER", "admin"),
		password:    password,
		tokenSecret: []byte(secret),
	}, nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", a.handleLogin)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("GET /api/records", a.auth(http.HandlerFunc(a.handleListRecords)))
	mux.Handle("POST /api/records", a.auth(http.HandlerFunc(a.handleCreateRecord)))
	mux.Handle("PATCH /api/records/{id}/return", a.auth(http.HandlerFunc(a.handleReturnRecord)))
	mux.Handle("GET /api/reports/daily", a.auth(http.HandlerFunc(a.handleDailyReport)))
	return mux
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

	token, err := a.signToken(req.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao criar sessao")
		return
	}

	writeJSON(w, http.StatusOK, LoginResponse{Token: token, User: req.Username})
}

func (a *App) handleListRecords(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if _, err := parseReportDate(date); err != nil {
		writeError(w, http.StatusBadRequest, "Data invalida")
		return
	}

	records, err := a.findRecords(r.Context(), date)
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

	now := time.Now().UTC()
	record := Record{
		ID:           id,
		PersonName:   req.PersonName,
		ItemName:     req.ItemName,
		Quantity:     req.Quantity,
		Notes:        req.Notes,
		Status:       "pending",
		CheckoutAt:   now,
		CheckoutDate: now.Format("2006-01-02"),
		CreatedBy:    user,
	}

	item, err := attributevalue.MarshalMap(record)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao preparar registro")
		return
	}
	if _, err := a.db.PutItem(r.Context(), &dynamodb.PutItemInput{
		TableName:           aws.String(a.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	}); err != nil {
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

	now := time.Now().UTC()
	result, err := a.db.UpdateItem(r.Context(), &dynamodb.UpdateItemInput{
		TableName: aws.String(a.table),
		Key: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberS{Value: id},
		},
		ConditionExpression: aws.String("attribute_exists(id)"),
		UpdateExpression:    aws.String("SET #status = :status, returnedAt = :returnedAt"),
		ExpressionAttributeNames: map[string]string{
			"#status": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":status":     &types.AttributeValueMemberS{Value: "returned"},
			":returnedAt": &types.AttributeValueMemberS{Value: now.Format(time.RFC3339Nano)},
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		var conditionErr *types.ConditionalCheckFailedException
		if errors.As(err, &conditionErr) {
			writeError(w, http.StatusNotFound, "Registro nao encontrado")
			return
		}
		writeError(w, http.StatusInternalServerError, "Erro ao salvar devolucao")
		return
	}

	var record Record
	if err := attributevalue.UnmarshalMap(result.Attributes, &record); err != nil {
		writeError(w, http.StatusInternalServerError, "Erro ao ler registro")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (a *App) handleDailyReport(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := parseReportDate(date); err != nil {
		writeError(w, http.StatusBadRequest, "Data invalida")
		return
	}

	records, err := a.findRecords(r.Context(), date)
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

		user, err := a.verifyToken(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "Sessao invalida")
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey{}, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", env("CORS_ORIGIN", "*"))
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

func (a *App) signToken(user string) (string, error) {
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Unix()
	payload := fmt.Sprintf("%s|%d", user, expiresAt)
	signature := a.sign(payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + signature, nil
}

func (a *App) verifyToken(token string) (string, error) {
	payloadPart, signature, ok := strings.Cut(token, ".")
	if !ok {
		return "", errors.New("token invalido")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return "", err
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(signature), []byte(a.sign(payload))) {
		return "", errors.New("assinatura invalida")
	}
	user, expiresAtRaw, ok := strings.Cut(payload, "|")
	if !ok || user == "" {
		return "", errors.New("payload invalido")
	}
	expiresAt, err := strconv.ParseInt(expiresAtRaw, 10, 64)
	if err != nil {
		return "", err
	}
	if time.Now().UTC().Unix() > expiresAt {
		return "", errors.New("token expirado")
	}
	return user, nil
}

func (a *App) sign(payload string) string {
	mac := hmac.New(sha256.New, a.tokenSecret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func serverAddress() string {
	if addr := env("APP_ADDR", ""); addr != "" {
		return addr
	}
	if port := env("PORT", ""); port != "" {
		if strings.HasPrefix(port, ":") {
			return port
		}
		return ":" + port
	}
	return ":8080"
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

func connectDynamo(ctx context.Context) (*dynamodb.Client, error) {
	region := env("AWS_REGION", "us-east-1")
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}

	endpoint := env("DYNAMODB_ENDPOINT", "")
	if endpoint == "" {
		return dynamodb.NewFromConfig(cfg), nil
	}

	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	}), nil
}

func (a *App) findRecords(ctx context.Context, date string) ([]Record, error) {
	input := &dynamodb.ScanInput{
		TableName: aws.String(a.table),
	}
	if strings.TrimSpace(date) != "" {
		input.FilterExpression = aws.String("checkoutDate = :date")
		input.ExpressionAttributeValues = map[string]types.AttributeValue{
			":date": &types.AttributeValueMemberS{Value: date},
		}
	}

	var records []Record
	paginator := dynamodb.NewScanPaginator(a.db, input)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		var pageRecords []Record
		if err := attributevalue.UnmarshalListOfMaps(page.Items, &pageRecords); err != nil {
			return nil, err
		}
		records = append(records, pageRecords...)
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].CheckoutAt.After(records[j].CheckoutAt)
	})
	if records == nil {
		return []Record{}, nil
	}
	return records, nil
}

func parseReportDate(date string) (time.Time, error) {
	if strings.TrimSpace(date) == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02", date)
}
