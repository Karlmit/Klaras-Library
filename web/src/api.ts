// Typed client for the Klaras Library API.
//
// Everything is same-origin, so the session cookie travels automatically and
// there is no token to manage in the browser.

export interface BookListItem {
  id: number
  uuid: string
  title: string
  authors: string[]
  series?: string
  series_index?: number
  rating?: number
  has_cover: boolean
  needs_review: boolean
  pub_year?: number
  added_at: string
}

export interface BookPage {
  items: BookListItem[]
  next_cursor?: string
  total?: number
}

export interface BookFile {
  format: string
  filename: string
  size_bytes: number
}

export interface ShelfRef {
  id: number
  name: string
  kobo_sync: boolean
}

export interface Book extends BookListItem {
  title_sort: string
  author_sort: string
  description?: string
  publisher?: string
  pubdate?: string
  tags: string[]
  languages: string[]
  path: string
  files: BookFile[]
  identifiers: { scheme: string; value: string }[]
  review_reasons?: string[]
  updated_at: string
  shelves?: ShelfRef[]
}

export interface Facet {
  value: string
  count: number
}

export interface Facets {
  authors: Facet[]
  tags: Facet[]
  languages: Facet[]
  series: Facet[]
  formats: Facet[]
  total_books: number
  needs_review: number
  refreshed_at?: string
}

export interface User {
  id: number
  username: string
  email?: string
  role: 'admin' | 'editor' | 'reader'
  locale: string
  password_reset_required: boolean
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  if (!res.ok) {
    let msg = res.statusText
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) msg = body.error
    } catch {
      // non-JSON error body; the status text will do
    }
    throw new ApiError(res.status, msg)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export interface BookQuery {
  q?: string
  author?: string
  tag?: string
  series?: string
  language?: string
  format?: string
  shelf?: number
  needs_review?: boolean
  sort?: string
  limit?: number
  cursor?: string
  total?: boolean
}

export function bookQueryString(qy: BookQuery): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(qy)) {
    if (v === undefined || v === '' || v === false) continue
    p.set(k, String(v))
  }
  return p.toString()
}

export const api = {
  status: () => req<{ needs_setup: boolean; version: string }>('/api/status'),
  me: () => req<{ authenticated: boolean; user?: User }>('/api/auth/me'),

  login: (username: string, password: string) =>
    req<User>('/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),

  logout: () => req<unknown>('/api/auth/logout', { method: 'POST' }),

  setup: (username: string, email: string, password: string) =>
    req<User>('/api/setup', {
      method: 'POST',
      body: JSON.stringify({ username, email, password }),
    }),

  changePassword: (current_password: string, new_password: string) =>
    req<unknown>('/api/auth/password', {
      method: 'POST',
      body: JSON.stringify({ current_password, new_password }),
    }),

  books: (qy: BookQuery) => req<BookPage>(`/api/books?${bookQueryString(qy)}`),
  book: (id: number) => req<Book>(`/api/books/${id}`),
  facets: () => req<Facets>('/api/facets'),
  suggest: (q: string) =>
    req<{ suggestions: { kind: string; value: string; id: number }[] }>(
      `/api/suggest?q=${encodeURIComponent(q)}`,
    ),
}

export const coverUrl = (id: number, size: 'grid' | 'detail' = 'grid') =>
  `/api/books/${id}/cover/${size}`

export interface Shelf {
  id: number
  uuid: string
  name: string
  is_public: boolean
  kobo_sync: boolean
  book_count: number
  owner: string
  mine: boolean
}

export interface MetadataResult {
  source: string
  title: string
  authors?: string[]
  series?: string
  description?: string
  publisher?: string
  pubdate?: string
  language?: string
  tags?: string[]
  cover_url?: string
  score: number
}

export interface BookEdit {
  title?: string
  authors?: string[]
  series?: string
  series_index?: number
  publisher?: string
  pubdate?: string
  description?: string
  tags?: string[]
  languages?: string[]
  rating?: number
  needs_review?: boolean
}

export const shelvesApi = {
  list: () => req<{ shelves: Shelf[] }>('/api/shelves'),

  create: (name: string, kobo_sync: boolean) =>
    req<Shelf>('/api/shelves', {
      method: 'POST',
      body: JSON.stringify({ name, kobo_sync }),
    }),

  update: (id: number, patch: Partial<Pick<Shelf, 'name' | 'kobo_sync' | 'is_public'>>) =>
    req<unknown>(`/api/shelves/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  remove: (id: number) => req<unknown>(`/api/shelves/${id}`, { method: 'DELETE' }),

  setBooks: (id: number, add: number[], remove: number[]) =>
    req<{ added: number; removed: number }>(`/api/shelves/${id}/books`, {
      method: 'POST',
      body: JSON.stringify({ add, remove }),
    }),
}

export const editApi = {
  one: (id: number, edit: BookEdit) =>
    req<Book>(`/api/books/${id}`, { method: 'PATCH', body: JSON.stringify(edit) }),

  bulk: (ids: number[], edit: BookEdit, add_tags?: string[], remove_tags?: string[]) =>
    req<{ count: number }>('/api/books/bulk', {
      method: 'POST',
      body: JSON.stringify({ ids, edit, add_tags, remove_tags }),
    }),

  lookup: (bookId: number) =>
    req<{ results: MetadataResult[]; providers: string[] }>(
      `/api/metadata/search?book=${bookId}`,
    ),
}

export const koboApi = {
  tokens: () =>
    req<{ tokens: { id: number; label: string; created_at: string; last_used_at: string | null }[] }>(
      '/api/kobo/tokens',
    ),
  create: (label: string) =>
    req<{ token: string; api_store_url: string }>('/api/kobo/tokens', {
      method: 'POST',
      body: JSON.stringify({ label }),
    }),
}

export const downloadUrl = (id: number, format: string) =>
  `/api/books/${id}/download/${format.toLowerCase()}`
