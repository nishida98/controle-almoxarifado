package api

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
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
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

func New(ctx context.Context) (*App, error) {
	client, err := connectDynamo(ctx)
	if err != nil {
		return nil, err
	}

	table := Env("DYNAMODB_TABLE", "")
	if table == "" {
		return nil, errors.New("DYNAMODB_TABLE nao configurada")
	}

	password := Env("APP_PASSWORD", "admin123")
	secret := Env("APP_TOKEN_SECRET", password)
	if len(secret) < 16 {
		log.Print("APP_TOKEN_SECRET nao configurado ou curto; usando APP_PASSWORD como segredo do token")
	}

	log.Printf("dynamodb table=%s", table)
	return &App{
		db:          client,
		table:       table,
		user:        Env("APP_USER", "admin"),
		password:    password,
		tokenSecret: []byte(secret),
	}, nil
}

func (a *App) Health(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return JSON(200, map[string]string{"status": "ok"}), nil
}

func (a *App) Login(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	var body LoginRequest
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return Error(400, "JSON invalido"), nil
	}
	if body.Username != a.user || body.Password != a.password {
		return Error(401, "Usuario ou senha invalidos"), nil
	}

	token, err := a.signToken(body.Username)
	if err != nil {
		return Error(500, "Erro ao criar sessao"), nil
	}
	return JSON(200, LoginResponse{Token: token, User: body.Username}), nil
}

func (a *App) ListRecords(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	if _, err := a.authUser(req); err != nil {
		return Error(401, "Sessao invalida"), nil
	}

	date := req.QueryStringParameters["date"]
	if _, err := parseReportDate(date); err != nil {
		return Error(400, "Data invalida"), nil
	}

	records, err := a.findRecords(ctx, date)
	if err != nil {
		return Error(500, "Erro ao buscar registros"), nil
	}
	return JSON(200, records), nil
}

func (a *App) WriteRecord(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	user, err := a.authUser(req)
	if err != nil {
		return Error(401, "Sessao invalida"), nil
	}

	switch req.RequestContext.HTTP.Method {
	case "POST":
		return a.createRecord(ctx, req, user)
	case "PATCH":
		return a.returnRecord(ctx, req)
	default:
		return Error(405, "Metodo nao permitido"), nil
	}
}

func (a *App) DailyReport(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	if _, err := a.authUser(req); err != nil {
		return Error(401, "Sessao invalida"), nil
	}

	date := req.QueryStringParameters["date"]
	if date == "" {
		date = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := parseReportDate(date); err != nil {
		return Error(400, "Data invalida"), nil
	}

	records, err := a.findRecords(ctx, date)
	if err != nil {
		return Error(500, "Erro ao buscar relatorio"), nil
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

	return JSON(200, DailyReport{
		Date:     date,
		ByPerson: buckets(byPerson),
		ByItem:   buckets(byItem),
		Records:  records,
		Summary:  summary,
	}), nil
}

func (a *App) createRecord(ctx context.Context, req events.APIGatewayV2HTTPRequest, user string) (events.APIGatewayV2HTTPResponse, error) {
	var body CreateRecordRequest
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return Error(400, "JSON invalido"), nil
	}
	body.PersonName = strings.TrimSpace(body.PersonName)
	body.ItemName = strings.TrimSpace(body.ItemName)
	body.Notes = strings.TrimSpace(body.Notes)
	if body.PersonName == "" || body.ItemName == "" {
		return Error(400, "Pessoa e item sao obrigatorios"), nil
	}
	if body.Quantity <= 0 {
		body.Quantity = 1
	}

	id, err := newToken()
	if err != nil {
		return Error(500, "Erro ao criar registro"), nil
	}

	now := time.Now().UTC()
	record := Record{
		ID:           id,
		PersonName:   body.PersonName,
		ItemName:     body.ItemName,
		Quantity:     body.Quantity,
		Notes:        body.Notes,
		Status:       "pending",
		CheckoutAt:   now,
		CheckoutDate: now.Format("2006-01-02"),
		CreatedBy:    user,
	}

	item, err := attributevalue.MarshalMap(record)
	if err != nil {
		return Error(500, "Erro ao preparar registro"), nil
	}
	if _, err := a.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(a.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	}); err != nil {
		return Error(500, "Erro ao salvar registro"), nil
	}
	return JSON(201, record), nil
}

func (a *App) returnRecord(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	id := strings.TrimSpace(req.PathParameters["id"])
	if id == "" {
		return Error(400, "ID obrigatorio"), nil
	}

	now := time.Now().UTC()
	result, err := a.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
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
			return Error(404, "Registro nao encontrado"), nil
		}
		return Error(500, "Erro ao salvar devolucao"), nil
	}

	var record Record
	if err := attributevalue.UnmarshalMap(result.Attributes, &record); err != nil {
		return Error(500, "Erro ao ler registro"), nil
	}
	return JSON(200, record), nil
}

func (a *App) authUser(req events.APIGatewayV2HTTPRequest) (string, error) {
	header := header(req.Headers, "authorization")
	token := strings.TrimPrefix(header, "Bearer ")
	if token == header || token == "" {
		return "", errors.New("sessao obrigatoria")
	}
	return a.verifyToken(token)
}

func header(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
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

func JSON(status int, value any) events.APIGatewayV2HTTPResponse {
	body, _ := json.Marshal(value)
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers: map[string]string{
			"Content-Type":                 "application/json",
			"Access-Control-Allow-Origin":  Env("CORS_ORIGIN", "*"),
			"Access-Control-Allow-Headers": "Content-Type, Authorization",
			"Access-Control-Allow-Methods": "GET, POST, PATCH, OPTIONS",
		},
		Body: string(body),
	}
}

func Error(status int, message string) events.APIGatewayV2HTTPResponse {
	return JSON(status, map[string]string{"error": message})
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

func Env(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func LoadEnvFile(path string) {
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
	region := Env("AWS_REGION", "us-east-1")
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, err
	}

	endpoint := Env("DYNAMODB_ENDPOINT", "")
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
