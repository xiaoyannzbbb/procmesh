export const CSRF_STORAGE_KEY = "procmesh_csrf";

export function getCsrf(): string | null {
  try {
    return sessionStorage.getItem(CSRF_STORAGE_KEY);
  } catch {
    return null;
  }
}

export function saveCsrf(token: string): void {
  sessionStorage.setItem(CSRF_STORAGE_KEY, token);
}

export function clearCsrf(): void {
  sessionStorage.removeItem(CSRF_STORAGE_KEY);
}
