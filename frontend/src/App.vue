<script setup>
import { computed, onMounted, reactive, ref } from 'vue'

const token = ref(localStorage.getItem('token') || '')
const user = ref(localStorage.getItem('user') || '')
const loading = ref(false)
const error = ref('')
const reportDate = ref(new Date().toISOString().slice(0, 10))
const statusFilter = ref('all')
const records = ref([])
const report = ref(null)
const apiBaseUrl = import.meta.env.VITE_API_URL || ''

const loginForm = reactive({
  username: 'admin',
  password: 'admin123'
})

const recordForm = reactive({
  personName: '',
  itemName: '',
  quantity: 1,
  notes: ''
})

const isLoggedIn = computed(() => Boolean(token.value))
const pendingRecords = computed(() => records.value.filter((record) => record.status === 'pending'))
const filteredRecords = computed(() => {
  if (statusFilter.value === 'all') {
    return records.value
  }
  return records.value.filter((record) => record.status === statusFilter.value)
})

async function api(path, options = {}) {
  const response = await fetch(`${apiBaseUrl}${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(token.value ? { Authorization: `Bearer ${token.value}` } : {}),
      ...(options.headers || {})
    }
  })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    throw new Error(data.error || 'Erro na requisicao')
  }
  return data
}

async function login() {
  error.value = ''
  loading.value = true
  try {
    const data = await api('/api/login', {
      method: 'POST',
      body: JSON.stringify(loginForm)
    })
    token.value = data.token
    user.value = data.user
    localStorage.setItem('token', data.token)
    localStorage.setItem('user', data.user)
    await refresh()
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}

function logout() {
  token.value = ''
  user.value = ''
  records.value = []
  report.value = null
  localStorage.removeItem('token')
  localStorage.removeItem('user')
}

async function createRecord() {
  error.value = ''
  loading.value = true
  try {
    await api('/api/records', {
      method: 'POST',
      body: JSON.stringify({
        personName: recordForm.personName,
        itemName: recordForm.itemName,
        quantity: Number(recordForm.quantity) || 1,
        notes: recordForm.notes
      })
    })
    recordForm.personName = ''
    recordForm.itemName = ''
    recordForm.quantity = 1
    recordForm.notes = ''
    await refresh()
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}

async function markReturned(record) {
  error.value = ''
  loading.value = true
  try {
    await api(`/api/records/${record.id}/return`, { method: 'PATCH' })
    await refresh()
  } catch (err) {
    error.value = err.message
  } finally {
    loading.value = false
  }
}

async function refresh() {
  const query = new URLSearchParams({ date: reportDate.value })
  const [recordsData, reportData] = await Promise.all([
    api(`/api/records?${query}`),
    api(`/api/reports/daily?${query}`)
  ])
  records.value = recordsData
  report.value = reportData
}

function formatDate(value) {
  return new Intl.DateTimeFormat('pt-BR', {
    dateStyle: 'short',
    timeStyle: 'short'
  }).format(new Date(value))
}

onMounted(() => {
  if (isLoggedIn.value) {
    refresh().catch(() => logout())
  }
})
</script>

<template>
  <main class="app-shell">
    <section v-if="!isLoggedIn" class="login-screen">
      <form class="login-panel" @submit.prevent="login">
        <p class="eyebrow">Almoxarifado</p>
        <h1>Controle de entradas e saidas</h1>
        <label>
          Usuario
          <input v-model="loginForm.username" autocomplete="username" />
        </label>
        <label>
          Senha
          <input v-model="loginForm.password" type="password" autocomplete="current-password" />
        </label>
        <p v-if="error" class="error">{{ error }}</p>
        <button :disabled="loading" type="submit">{{ loading ? 'Entrando...' : 'Entrar' }}</button>
      </form>
    </section>

    <section v-else class="workspace">
      <header class="topbar">
        <div>
          <p class="eyebrow">Controle Almoxarifado</p>
          <h1>Saidas e devolucoes</h1>
        </div>
        <div class="user-actions">
          <span>{{ user }}</span>
          <button class="secondary" type="button" @click="logout">Sair</button>
        </div>
      </header>

      <p v-if="error" class="error">{{ error }}</p>

      <section class="metrics" v-if="report">
        <article>
          <span>Registros</span>
          <strong>{{ report.summary.totalRecords }}</strong>
        </article>
        <article>
          <span>Pendentes</span>
          <strong>{{ report.summary.pending }}</strong>
        </article>
        <article>
          <span>Devolvidos</span>
          <strong>{{ report.summary.returned }}</strong>
        </article>
        <article>
          <span>Itens retirados</span>
          <strong>{{ report.summary.totalItems }}</strong>
        </article>
      </section>

      <div class="content-grid">
        <form class="form-panel" @submit.prevent="createRecord">
          <h2>Novo registro</h2>
          <label>
            Pessoa
            <input v-model="recordForm.personName" required placeholder="Nome de quem retirou" />
          </label>
          <label>
            Item
            <input v-model="recordForm.itemName" required placeholder="Ex: furadeira, cabo, luva" />
          </label>
          <div class="split">
            <label>
              Quantidade
              <input v-model="recordForm.quantity" min="1" type="number" />
            </label>
          </div>
          <label>
            Observacoes
            <textarea v-model="recordForm.notes" rows="3" placeholder="Opcional"></textarea>
          </label>
          <button :disabled="loading" type="submit">Registrar saida</button>
        </form>

        <section class="list-panel">
          <div class="panel-heading">
            <div>
              <h2>Movimentacoes do dia</h2>
              <p>
                {{ pendingRecords.length }} pendente(s) |
                {{ filteredRecords.length }} exibido(s)
              </p>
            </div>
            <div class="filters">
              <input v-model="reportDate" type="date" @change="refresh" />
              <select v-model="statusFilter" aria-label="Filtrar por status">
                <option value="all">Todos</option>
                <option value="pending">Pendentes</option>
                <option value="returned">Devolvidos</option>
              </select>
            </div>
          </div>

          <div class="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Pessoa</th>
                  <th>Item</th>
                  <th>Qtd.</th>
                  <th>Status</th>
                  <th>Saida</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="record in filteredRecords" :key="record.id">
                  <td>{{ record.personName }}</td>
                  <td>
                    {{ record.itemName }}
                    <small v-if="record.notes">{{ record.notes }}</small>
                  </td>
                  <td>{{ record.quantity }}</td>
                  <td>
                    <span class="badge" :class="record.status">
                      {{ record.status === 'returned' ? 'Devolvido' : 'Pendente' }}
                    </span>
                  </td>
                  <td>{{ formatDate(record.checkoutAt) }}</td>
                  <td>
                    <button
                      v-if="record.status !== 'returned'"
                      class="secondary"
                      type="button"
                      @click="markReturned(record)"
                    >
                      Devolver
                    </button>
                  </td>
                </tr>
                <tr v-if="filteredRecords.length === 0">
                  <td colspan="6" class="empty">Nenhuma movimentacao nesta data.</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </div>

      <section class="reports" v-if="report">
        <div>
          <h2>Relatorio por pessoa</h2>
          <article v-for="person in report.byPerson" :key="person.name" class="report-row">
            <strong>{{ person.name }}</strong>
            <span>{{ person.total }} itens | {{ person.pending }} pendentes | {{ person.returned }} devolvidos</span>
          </article>
        </div>
        <div>
          <h2>Relatorio por item</h2>
          <article v-for="item in report.byItem" :key="item.name" class="report-row">
            <strong>{{ item.name }}</strong>
            <span>{{ item.total }} retirados | {{ item.pending }} pendentes | {{ item.returned }} devolvidos</span>
          </article>
        </div>
      </section>
    </section>
  </main>
</template>
