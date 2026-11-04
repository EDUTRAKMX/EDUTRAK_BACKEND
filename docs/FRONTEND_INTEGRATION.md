# Guía de integración Frontend ↔ Backend

El contrato exacto de cada endpoint está en [`api/openapi.yaml`](../api/openapi.yaml) (validado por pruebas contra las respuestas reales). Esta guía explica **cómo consumirlo**; no repite el contrato.

## 1. Conexión
- **URL base:** la de la API (`http://localhost:8080` en desarrollo). Todo el negocio cuelga de `/api/...`; `GET /healthz` y `GET /readyz` no requieren token.
- **CORS:** el backend solo responde cabeceras CORS a los orígenes de `CORS_ALLOWED_ORIGINS` (exactos, sin `*`). Para Vite: `CORS_ALLOWED_ORIGINS=http://localhost:5173`. Un origen no listado falla en el navegador con error CORS aunque la petición llegue. Alternativa sin CORS: servir el frontend y `/api` desde el mismo origen con un proxy.
- **No hay cookies**: la autenticación es `Authorization: Bearer <access_token>`. No uses `credentials: 'include'`.
- **Cuerpos JSON:** envía siempre `Content-Type: application/json`. Otro tipo → 415; JSON mal formado o con campos que el endpoint no define → 400 `invalid_body` (el backend rechaza campos desconocidos: no mandes `student_id`, `recorded_by`, `tutor_id` del actor, etc. donde el token ya identifica a la persona).

## 2. Sesión
```
POST /api/auth/login   {institutional_number, password}
 → {token_type:"Bearer", access_token, access_expires_at, refresh_token, refresh_expires_at, user:{id, roles[], must_change_password…}}
```
- **Access token:** corto (15 min por defecto). Envíalo en cada petición. **Refresh token:** de **un solo uso**; `POST /api/auth/refresh {refresh_token}` devuelve un par nuevo y el anterior queda inservible.
- **Renovación segura:** renueva cuando una petición responda 401 (o poco antes de `access_expires_at`) y **serializa** la renovación (una sola llamada a `/refresh` en vuelo; las demás peticiones esperan su resultado y se reintentan una vez). Dos pestañas que renueven a la vez con el mismo refresh token: el backend tolera el reintento durante 10 s; pasado ese plazo, reutilizar un token ya rotado **revoca todas las sesiones** del usuario (señal de robo). Si `/refresh` responde 401 → pantalla de login.
- **Dónde guardar tokens:** el refresh token es la credencial valiosa; guárdalo en memoria o almacenamiento del navegador con las precauciones de tu app (ver riesgo XSS), nunca en la URL ni en logs.
- **`must_change_password: true`** (cuenta nueva o con reset del ADMIN): solo funcionan `GET /api/auth/me`, `POST /api/auth/logout` y `PATCH /api/auth/password`; el resto responde **403** con código `password_change_required`. Redirige a "cambiar contraseña" (`{current_password, new_password}` ≥ 10 caracteres). Tras cambiarla, las demás sesiones del usuario se cierran y la actual sigue válida.
- **Logout:** `POST /api/auth/logout` (204) y olvida ambos tokens.
- **Roles** (`user.roles`, puede haber varios): `ADMIN` (solo cuentas), `DIRECTOR`, `TEACHER`, `TUTOR`, `STUDENT`. Úsalos para decidir qué menús mostrar, pero **la autorización real la decide el backend** por rol *y relación* (ser TUTOR no basta: debe ser el tutor de ese grupo ahora). Un 403 en una pantalla "permitida por rol" es normal (p. ej. el tutor anterior tras un cambio de tutor).

## 3. Errores
Todos: `{"error":{"code","message","request_id"}}`. Haz la lógica con `code` (estable), muestra `message` solo como apoyo y **registra `request_id`** en soporte.

| HTTP | Significado en la UI |
|---|---|
| 400 `invalid_body` | bug del cliente (JSON/campos) |
| 401 | sin sesión/expirada → renovar o login |
| 403 | sin permiso por rol/relación (o `password_change_required`) |
| 404 | no existe (también si el id no es UUID) |
| 409 | conflicto de estado: `term_closed`, `grade_exists`, `session_already_open`, `attendance_already_registered`, `absence_not_eligible`… → mensaje específico por `code` |
| 413 / 415 | archivo/cuerpo demasiado grande / tipo no soportado |
| 422 | validación del campo o referencia inexistente en el cuerpo; mostrar junto al formulario |
| 429 | demasiados intentos de login; respeta la cabecera `Retry-After` (segundos) |
| 500 | error interno; mostrar el `request_id` |

## 4. Convenciones de datos
- **Listas:** `{"data":[...]}`. Solo seguimientos de Tutoring y alertas de Analytics están paginados (`limit` 1–200, `offset`; la respuesta trae `total`, `limit`, `offset`). El resto devuelve todo lo visible para el rol.
- **Fechas:** `YYYY-MM-DD` (día en la zona de la institución, `America/Mexico_City` por defecto); instantes en RFC 3339 **UTC** (`2026-02-02T14:00:00Z`): conviértelos a hora local para mostrarlos. `session_date` ya es la fecha local de la clase. No mandes horas del cliente: el servidor usa la suya (puntualidad, vigencia del QR).
- **Calificaciones:** números con a lo sumo dos decimales (0–100) enviados y recibidos como número JSON (`89.99`). Para mostrar usa `toFixed(2)`. **No calcules promedios** (no hay política institucional). `category` (NA/SA/DE/AU) viene del backend; no la recalcules.
- **`null` significa "no hay dato", no cero:** `grade: null` = unidad sin capturar; `attendance_pct: null` = sin registros que cuenten; `lates_per_absence: null` = sin sesiones. Pinta un guion, nunca `0`.
- **Estados** (para chips/colores): periodo `draft|active|closed`; inscripción `ACTIVE|WITHDRAWN`; sesión `OPEN|CLOSED`; asistencia `PRESENT|LATE|ABSENT|JUSTIFIED`; justificante `PENDING_TUTOR|PENDING_TEACHER|TUTOR_REJECTED|TEACHER_REJECTED|JUSTIFIED`; alerta `OPEN|ACKNOWLEDGED|RESOLVED`.
- **ids:** UUID en cadena.

## 5. Flujos

### Asistencia con QR
1. **Profesor:** `POST /api/attendance/sessions {teaching_assignment_id}` → `id` de la sesión. Si la pantalla se recarga y pierdes el id, recupéralo con `GET /api/attendance/teaching-assignments/{id}` (lista de sesiones con su estado `OPEN`).
2. Cada ≈30 s (`expires_at` del último token, `QR_TOKEN_TTL`) llama `POST /api/attendance/sessions/{id}/qr/rotate` → `{token, expires_at}` y **dibuja un QR en el cliente** (el backend no genera imágenes) con el contenido que decidan, p. ej. `{"s":"<session_id>","t":"<token>"}`. Cada rotación invalida el token anterior.
3. **Alumno:** escanea, parsea y llama `POST /api/attendance/sessions/{id}/check-in {"token":"…"}` (solo ese campo). 201 → `status` `PRESENT` o `LATE` (según el servidor). Errores a mostrar: `qr_expired`/`invalid_qr_token` (pedir escanear de nuevo), `network_not_allowed` (**debe estar en la red/Wi-Fi institucional**), `attendance_already_registered` (ya registrado), `not_enrolled`, `session_closed`.
4. **Profesor:** captura manual `POST …/sessions/{id}/manual {subject_enrollment_id, status}`; corrección `PATCH /api/attendance/records/{id}`; cierre `POST …/sessions/{id}/close` (genera las ausencias y responde `absences_created`).
5. Consulta: `GET /api/attendance/sessions/{id}` (detalle en vivo), `…/teaching-assignments/{id}` (resumen), `…/students/{id}` (historial del alumno).

### Calificaciones
Profesor: tabla `GET /api/teaching-assignments/{id}/grades` (celda por unidad, `grade: null` si falta) → `POST /api/grades` para capturar (409 `grade_exists` si ya hay; entonces `PATCH /api/grades/{id}`). Alumno: `GET /api/students/{su id}/grades`.

### Justificantes
1. Alumno ve sus ausencias en `GET /api/attendance/students/{id}` (`status: ABSENT`) y crea la solicitud: JSON `{attendance_record_id, reason}` **o** `multipart/form-data` con los campos de texto `attendance_record_id` y `reason` **antes** de los archivos y hasta 5 en el campo `evidence` (PDF/JPG/PNG ≤ 10 MB; el tipo se valida por **contenido**, no por el nombre; en `FormData` agrega primero los campos de texto y después los archivos). No fijes tú el `Content-Type` de `FormData`: el navegador agrega el boundary.
2. Tutor: `POST /api/justifications/{id}/tutor-decision {decision, comment}` (comentario obligatorio al rechazar). Profesor: `…/teacher-decision`. Cada etapa se decide una vez; la bandeja de cada rol es `GET /api/justifications?status=PENDING_TUTOR|PENDING_TEACHER`. `awaiting_stage` dice quién debe actuar.
3. **Descarga de evidencia:** `GET /api/justifications/{id}/evidence/{evidenceId}` exige el `Authorization`, así que **un `<a href>` o `<img src>` no funciona**. Usa `fetch` con el token, toma el `blob()` y ábrelo con `URL.createObjectURL` (o fuerza descarga con el nombre de `original_filename`).
4. Una aprobación final puede responder 409 `absence_not_eligible` si el profesor ya corrigió la asistencia: refresca la solicitud y la ausencia.

### Tutoría
`GET /api/tutoring/students` (mis alumnos) → `GET /api/tutoring/students/{id}?group_id=` (resumen académico) → `POST /api/tutoring/follow-ups {student_id, group_id, note, followup_date?}`; historial `GET …/students/{id}/follow-ups`. Los seguimientos no se editan ni se borran. Pasa siempre `group_id` si el alumno tiene varios grupos/periodos.

### Analytics y alertas
- `GET /api/analytics/students/{id}?group_id=` y `/groups/{id}` calculan **en vivo** (`signals`, `signals_now`). `GET /api/analytics/alerts` devuelve las alertas **persistidas**, tan frescas como el último `POST /api/analytics/alerts/recalculate {group_id}`: ofrece un botón "Actualizar alertas" que lo llame y luego recargue la lista; muestra `evaluated_at`. Ninguna lectura recalcula por sí sola.
- `attendance_pct` es la **asistencia ajustada por justificaciones**: (asistencias+retardos) / (asistencias+retardos+faltas), con las faltas justificadas fuera del denominador. Rotúlalo así en la UI para no compararlo con "asistencias / sesiones totales".
- Los umbrales de las alertas (3 faltas, ventana de 5, caída de 20 puntos) son **propuestas del MVP**, no reglas oficiales; muestra el texto `message` de la alerta tal cual (ya cita las cifras y el umbral).
- Solo TUTOR (de ese grupo) y DIRECTOR acceden a Analytics; el resto recibe 403 (no hay vista para alumno ni profesor).

## 6. Cambios de relación mientras la app está abierta
Si el Director cambia el tutor de un grupo o el profesor de una asignación, el acceso cambia **al instante** en todos los módulos. La UI debe tolerar 403/404 en pantallas que antes funcionaban y refrescar sus listas.

## 7. Incompatibilidades y límites conocidos
- El token de evidencia/QR no puede ir en la URL (por diseño); ver descarga con `fetch`.
- No hay endpoint de listado de sesiones de asistencia por fecha ni de usuarios; para el selector de alumno/profesor usa las listas de Academic de tu rol.
- Los listados de Academic no están paginados (devuelven todo lo visible).
- `ADMIN` no ve datos académicos; su UI se limita a alta de usuarios y reseteo de contraseña.
- El backend no envía notificaciones ni eventos en tiempo real: usa sondeo/recarga donde lo necesites (p. ej. el panel de asistencia del profesor).
- La validación de red del check-in depende de la configuración `ALLOWED_NETWORKS` del despliegue; en desarrollo se desactiva con `ATTENDANCE_ALLOW_ANY_NETWORK=true`.
