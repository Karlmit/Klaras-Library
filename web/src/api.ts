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
  adult_reason?: string
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
  adult: number
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
  // 'only' shows nothing but flagged books; the server ignores it for
  // non-administrators, so this is a view, not a permission.
  adult?: 'only' | 'include'
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
  kobo_subscribed: boolean
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

  setAdult: (ids: number[], adult: boolean) =>
    req<{ changed: number; adult: boolean }>('/api/books/adult', {
      method: 'POST',
      body: JSON.stringify({ ids, adult }),
    }),
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

export interface DiscoverCard {
  id: number
  uuid: string
  title: string
  authors: string[]
  series?: string
  series_index?: number
  description?: string
  publisher?: string
  pub_year?: number
  rating?: number
  tags: string[]
  languages: string[]
  has_cover: boolean
  formats: string[]
}

export interface DiscoverStats {
  kept: number
  passed: number
  remaining: number
  shelf_id: number
  shelf_name: string
}

export const discoverApi = {
  deck: (limit = 8) =>
    req<{ cards: DiscoverCard[]; stats: DiscoverStats }>(`/api/discover?limit=${limit}`),
  decide: (book_id: number, action: 'keep' | 'pass' | 'undo') =>
    req<{ stats: DiscoverStats }>('/api/discover', {
      method: 'POST',
      body: JSON.stringify({ book_id, action }),
    }),
}

export interface DescriptionStatus {
  total: number
  with_description: number
  missing: number
  missing_with_isbn: number
  remaining: number
  unreachable: number
  last_run?: string
  found_in_files: number
  found_via_google: number
  asked_google: number
  recent: { day: string; found: number; asked: number }[]
  google_enabled: boolean
  running: boolean
}

export const descriptionsApi = {
  status: () => req<DescriptionStatus>('/api/descriptions'),
  run: () => req<{ status: string }>('/api/descriptions/run', { method: 'POST' }),
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
  // Omit userId to reset your own devices; admins may pass someone else's.
  resync: (userId?: number) =>
    req<{ forgotten: number }>('/api/kobo/resync', {
      method: 'POST',
      body: JSON.stringify(userId ? { user_id: userId } : {}),
    }),
}

export const downloadUrl = (id: number, format: string) =>
  `/api/books/${id}/download/${format.toLowerCase()}`

export interface UserSummary {
  id: number
  username: string
  email?: string
  role: 'admin' | 'editor' | 'reader'
  is_active: boolean
  needs_password: boolean
  shelves: number
  kobo_tokens: number
  created_at: string
}

export const usersApi = {
  list: () => req<{ users: UserSummary[] }>('/api/users'),

  update: (id: number, patch: { role?: string; is_active?: boolean }) =>
    req<unknown>(`/api/users/${id}`, { method: 'PATCH', body: JSON.stringify(patch) }),

  setPassword: (id: number, password: string) =>
    req<unknown>(`/api/users/${id}/password`, {
      method: 'PUT',
      body: JSON.stringify({ password }),
    }),
}

export const deleteApi = {
  one: (id: number, keepFiles = false) =>
    req<{ files_removed: number; directory_removed: boolean }>(
      `/api/books/${id}${keepFiles ? '?keep_files=1' : ''}`,
      { method: 'DELETE' },
    ),

  bulk: (ids: number[], keepFiles = false) =>
    req<{ deleted: number; failed: number; files_removed: number }>('/api/books/bulk-delete', {
      method: 'POST',
      body: JSON.stringify({ ids, keep_files: keepFiles }),
    }),
}

export interface UploadResult {
  filename: string
  book_id?: number
  status: 'imported' | 'duplicate' | 'failed'
  error?: string
}

export interface ReadingProgress {
  status: 'ReadyToRead' | 'Reading' | 'Finished'
  percent?: number
  location?: string
}

/** Multipart uploads must not carry a JSON Content-Type; the browser sets the boundary. */
async function upload<T>(path: string, form: FormData, method = 'POST'): Promise<T> {
  const res = await fetch(path, { method, body: form })
  if (!res.ok) {
    let msg = res.statusText
    try {
      const b = (await res.json()) as { error?: string }
      if (b.error) msg = b.error
    } catch {
      // non-JSON body; the status text will do
    }
    throw new ApiError(res.status, msg)
  }
  return (await res.json()) as T
}

export const booksApi = {
  upload: (files: File[]) => {
    const form = new FormData()
    for (const f of files) form.append('file', f)
    return upload<{ results: UploadResult[] }>('/api/books/upload', form)
  },

  fetchCover: (id: number, url: string) =>
    req<{ status: string }>(`/api/books/${id}/cover/fetch`, {
      method: 'POST',
      body: JSON.stringify({ url }),
    }),
  replaceCover: (id: number, file: File) => {
    const form = new FormData()
    form.append('file', file)
    return upload<{ status: string }>(`/api/books/${id}/cover`, form, 'PUT')
  },

  progress: (id: number) => req<ReadingProgress>(`/api/books/${id}/progress`),

  saveProgress: (id: number, p: ReadingProgress) =>
    req<unknown>(`/api/books/${id}/progress`, { method: 'PUT', body: JSON.stringify(p) }),
}

export const createUser = (
  username: string, email: string, password: string, role: string,
) => req<User>('/api/users', {
  method: 'POST',
  body: JSON.stringify({ username, email, password, role }),
})

export const selectionApi = {
  /** Every book id matching a filter, for "select all". */
  ids: (qy: BookQuery) =>
    req<{ ids: number[]; count: number; truncated: boolean; limit: number }>(
      `/api/books/ids?${bookQueryString(qy)}`,
    ),
}

export const koboShelfApi = {
  subscribe: (shelfId: number) =>
    req<unknown>(`/api/shelves/${shelfId}/kobo-subscription`, { method: 'POST' }),
  unsubscribe: (shelfId: number) =>
    req<unknown>(`/api/shelves/${shelfId}/kobo-subscription`, { method: 'DELETE' }),
}
