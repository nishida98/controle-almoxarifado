# Controle Almoxarifado

Projeto simples para controlar entrada e saida de itens do almoxarifado.

## Estrutura

- `backend/`: API em Go para AWS Lambda com DynamoDB.
- `frontend/`: aplicacao Vue.js com Vite.

## Como rodar

### Backend

```bash
cd backend
go run .
```

A API sobe em `http://localhost:8080`.

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

Build do backend:

```bash
go build -tags netgo -ldflags "-s -w" -o app
```

### Deploy AWS Lambda

O backend tambem esta preparado para rodar como Lambda em container.

Arquivos principais:

- `backend/Dockerfile`
- `backend/template.yaml`

Build local da imagem:

```bash
cd backend
docker build -t controle-almoxarifado-api .
```

Deploy com AWS SAM:

```bash
cd backend
sam build
sam deploy --guided
```

O template cria:

- uma Lambda container para a API HTTP
- uma tabela DynamoDB on-demand
- um HTTP API Gateway roteando `/{proxy+}`

Configure no deploy:

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
