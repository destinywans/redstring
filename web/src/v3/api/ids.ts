/** Service IDs are base64url of "application NUL environment" (both persisted row IDs). */
export function serviceParts(serviceId: string): { application: string; environment: string } | null {
  try {
    const raw = atob(serviceId.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(serviceId.length / 4) * 4, "="));
    const i = raw.indexOf("\u0000");
    return i > 0 ? { application: raw.slice(0, i), environment: raw.slice(i + 1) } : null;
  } catch {
    return null;
  }
}

/** The application part ties a service to investigations, delivery runs and alert rules. */
export const serviceApplicationId = (serviceId: string) => serviceParts(serviceId)?.application ?? null;
