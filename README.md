# Controle Almoxarifado

Projeto simples para controlar entrada e saida de itens do almoxarifado.

## Estrutura

- `backend/`: API em Go para AWS Lambda com DynamoDB.
- `frontend/`: aplicacao Vue.js com Vite.

## Como rodar

### Backend local

```bash
cd backend
go test ./...
```

As funcoes foram separadas para deploy em Lambda. Para execucao local completa da API, use AWS SAM:

```bash
cd backend
sam build
sam local start-api
```

Login padrao:

- Usuario: `admin`
- Senha: `admin123`

Voce pode alterar com variaveis de ambiente:

- `APP_USER`
- `APP_PASSWORD`
- `APP_TOKEN_SECRET`
- `APP_ADDR`
- `PORT` (usado se `APP_ADDR` nao estiver definido)
- `AWS_REGION`
- `DYNAMODB_TABLE`
- `DYNAMODB_ENDPOINT` (opcional, para DynamoDB local)
- `CORS_ORIGIN`

Voce pode criar um arquivo `backend/.env` baseado em `backend/.env.example`. Esse arquivo fica ignorado pelo Git.

No PowerShell:

```powershell
$env:AWS_REGION="us-east-1"
$env:DYNAMODB_TABLE="controle-almoxarifado-records"
go run .
```

Build local das funcoes:

```bash
go build -tags lambda.norpc -ldflags "-s -w" -o bootstrap-login ./cmd/login
go build -tags lambda.norpc -ldflags "-s -w" -o bootstrap-records-get ./cmd/records-get
go build -tags lambda.norpc -ldflags "-s -w" -o bootstrap-records-write ./cmd/records-write
go build -tags lambda.norpc -ldflags "-s -w" -o bootstrap-report ./cmd/report
```

### Deploy AWS Lambda

O backend roda como quatro Lambdas em containers separados.

Arquivos principais:

- `backend/Dockerfile.login`
- `backend/Dockerfile.records-get`
- `backend/Dockerfile.records-write`
- `backend/Dockerfile.report`
- `backend/template.yaml`

Build local das imagens:

```bash
cd backend
docker build -f Dockerfile.login -t controle-almoxarifado-login .
docker build -f Dockerfile.records-get -t controle-almoxarifado-records-get .
docker build -f Dockerfile.records-write -t controle-almoxarifado-records-write .
docker build -f Dockerfile.report -t controle-almoxarifado-report .
```

Deploy com AWS SAM:

```bash
cd backend
sam build
sam deploy --guided
```

O template cria:

- uma Lambda container para login e health check
- uma Lambda container para listar registros
- uma Lambda container para criar registros e marcar devolucao
- uma Lambda container para relatorio diario
- uma tabela DynamoDB on-demand
- um HTTP API Gateway roteando cada endpoint para sua Lambda

Configure no deploy:

- `AppUser`
- `AppPassword`
- `AppTokenSecret`
- `CorsOrigin`

Esses parametros alimentam as variaveis:

- `APP_PASSWORD`
- `APP_TOKEN_SECRET`
- `CORS_ORIGIN`

### Frontend

```bash
cd frontend
npm install
npm run dev
```

O frontend sobe em `http://localhost:5173` e faz proxy para o backend.

Em producao, se o frontend ficar em um dominio diferente do backend, configure:

- `VITE_API_URL` com a URL publica do backend, por exemplo `https://sua-api.onrender.com`

Build do frontend:

```bash
npm run build
```
