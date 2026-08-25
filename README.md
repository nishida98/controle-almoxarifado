# Controle Almoxarifado

Projeto simples para controlar entrada e saida de itens do almoxarifado.

## Estrutura

- `backend/`: API em Go com armazenamento local em JSON.
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
- `APP_ADDR`
- `MONGODB_URI`
- `MONGODB_DATABASE` (padrao: `controle_almoxarifado`)
- `MONGODB_COLLECTION` (padrao: `records`)

Voce pode criar um arquivo `backend/.env` baseado em `backend/.env.example`. Esse arquivo fica ignorado pelo Git.

No PowerShell:

```powershell
$env:MONGODB_URI="mongodb+srv://usuario:senha@cluster.mongodb.net/?appName=Cluster0"
go run .
```

### Frontend

```bash
cd frontend
npm install
npm run dev
```

O frontend sobe em `http://localhost:5173` e faz proxy para o backend.
