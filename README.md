# EDUTRAK — Backend

API REST en Go (monolito modular, arquitectura hexagonal) para seguimiento académico y tutorial.

## Estado real

| Área | Estado |
|---|---|
| Bootstrap (config, logging, HTTP, graceful shutdown, pgx, migraciones) | Implementado |
| Esquema PostgreSQL (23 tablas) | Implementado (`migrations/`) |
| Identity (login, refresh, logout, me, cambio y reset de contraseña, alta de usuarios, RBAC) | Implementado |
| Audit (escritura en `audit_logs`) | Solo registro; sin endpoints de consulta |
| Academic (catálogos, periodos, grupos, inscripciones, asignaciones docentes, horarios, inscripciones a materia, consultas por rol) | Implementado (`/api/academic/*`) |
| Grades, Attendance, Justifications, Tutoring, Analytics | **No implementado** (solo tablas) |

## Requisitos
Go 1.27.1 (versión declarada en `go.mod`), PostgreSQL 16 (o Docker).

## Setup
```bash
cp .env.example .env          # editar JWT_SECRET (>= 32 bytes) y credenciales
docker compose up -d db db-test
go run ./cmd/migrate          # aplica migraciones
go run ./cmd/seed             # crea el primer ADMIN (SEED_ADMIN_NUMBER / SEED_ADMIN_PASSWORD)
go run ./cmd/api
```

## Variables
`DATABASE_URL` y `JWT_SECRET` son obligatorias (el arranque falla sin ellas). Opcionales: `HTTP_PORT` (8080), `ACCESS_TOKEN_TTL` (15m), `REFRESH_TOKEN_TTL` (168h), `QR_TOKEN_TTL` (30s), `LOGIN_MAX_FAILURES_PER_ACCOUNT` (5), `LOGIN_MAX_FAILURES_PER_IP` (150), `LOGIN_RATE_WINDOW` (15m), `ALLOWED_NETWORKS`, `TRUSTED_PROXIES`, `STORAGE_DIR`, `MAX_EVIDENCE_MB`, `APP_ENV`, `LOG_LEVEL`. Ver `.env.example`. Un archivo `.env` se carga si existe sin pisar variables ya definidas.

## Pruebas
```bash
go test ./... -race
# integración (requiere PostgreSQL; cada test usa un esquema aislado):
TEST_DATABASE_URL="postgres://edutrak:edutrak_dev_password@localhost:5433/edutrak_test?sslmode=disable" go test ./... -race
```
Sin `TEST_DATABASE_URL` las pruebas de integración se omiten.

## Endpoints
Salud: `GET /healthz`, `GET /readyz`.

| Método | Ruta | Acceso |
|---|---|---|
| POST | `/api/auth/login` | público |
| POST | `/api/auth/refresh` | refresh token (rota) |
| POST | `/api/auth/logout` | autenticado |
| GET | `/api/auth/me` | autenticado |
| PATCH | `/api/auth/password` | autenticado (requiere contraseña actual) |
| POST | `/api/admin/users` | ADMIN |
| PUT | `/api/admin/users/{id}/password` | ADMIN (contraseña temporal; revoca sesiones y fuerza cambio) |

### Academic (`/api/academic`)
Todas las rutas exigen `Authorization: Bearer <access_token>` (401 si falta o es inválido). La **autorización real** (rol + relación con el recurso) la deciden los casos de uso a partir de la identidad autenticada; ningún id de profesor, tutor o alumno enviado por el cliente sustituye al del token. Un `ADMIN` administra cuentas pero **no** accede a grupos, horarios ni inscripciones (403). Un recurso inexistente (o un id que no es UUID) responde 404; uno existente pero ajeno, 403. Las listas responden `{"data":[...]}`. Las rutas de abajo son relativas a `/api/academic`.

| Método | Ruta | Acceso |
|---|---|---|
| GET | `/careers`, `/careers/{id}`, `/careers/{id}/curriculum[?level=n]` | cualquier usuario autenticado (catálogo institucional) |
| POST | `/careers`, `/careers/{id}/curriculum` | DIRECTOR |
| GET | `/subjects`, `/subjects/{id}`, `/subjects/{id}/units` | cualquier usuario autenticado |
| POST | `/subjects`, `/subjects/{id}/units` | DIRECTOR |
| GET | `/terms`, `/terms/{id}` | DIRECTOR |
| POST | `/terms`, `/terms/{id}/activate`, `/terms/{id}/close` | DIRECTOR |
| GET | `/groups[?academic_term_id=]` | DIRECTOR: todos · TEACHER: donde tiene asignación · TUTOR: donde es tutor · STUDENT: donde está inscrito (activo) |
| POST | `/groups` | DIRECTOR |
| GET | `/groups/{id}` | DIRECTOR, tutor del grupo, profesor con asignación en él, alumno inscrito |
| PUT | `/groups/{id}/tutor` (`{"tutor_id": "uuid"}` o `null` para quitarlo) | DIRECTOR |
| GET | `/groups/{id}/enrollments` | DIRECTOR y tutor de ese grupo |
| POST | `/groups/{id}/enrollments` | DIRECTOR |
| GET | `/groups/{id}/teaching-assignments` | quien puede ver el grupo (cada rol ve solo sus asignaciones) |
| POST | `/groups/{id}/teaching-assignments` | DIRECTOR |
| GET | `/teaching-assignments[?group_id=]` | acotado por relación (DIRECTOR todas; profesor las suyas; tutor las de sus grupos; alumno aquellas donde tiene inscripción a materia); filtrar por un grupo ajeno da 403 |
| GET | `/teaching-assignments/{id}` | DIRECTOR, su profesor, tutor del grupo, alumno inscrito a la materia |
| PUT | `/teaching-assignments/{id}/teacher` | DIRECTOR |
| GET | `/teaching-assignments/{id}/students` | DIRECTOR y el profesor de esa asignación |
| GET / POST | `/teaching-assignments/{id}/schedules` | GET: como la asignación · POST: DIRECTOR |
| POST | `/teaching-assignments/{id}/subject-enrollments` (sin cuerpo) | DIRECTOR; inscribe a todo el grupo, idempotente, responde `{"created": n}` |
| GET | `/schedules[?group_id=]` | horario propio según rol (DIRECTOR todos) |
| GET | `/subject-enrollments[?teaching_assignment_id=&student_id=]` | DIRECTOR: todas (con filtros) · TEACHER: las de sus asignaciones · STUDENT: solo las propias (un `student_id` ajeno se ignora) |
| POST | `/subject-enrollments` | DIRECTOR |

Ejemplos (valores ficticios):
```http
POST /api/academic/terms
{"name":"2026-1","start_date":"2026-01-05","end_date":"2026-04-30"}
-> 201 {"id":"…","name":"2026-1","start_date":"2026-01-05","end_date":"2026-04-30","status":"draft"}

POST /api/academic/groups
{"academic_term_id":"…","career_id":"…","code":"2A","level":2,"tutor_id":"…"}
-> 201 {"id":"…","academic_term_id":"…","academic_term_name":"2026-1","career_id":"…","career_code":"TIC",
        "career_name":"…","tutor_id":"…","code":"2A","level":2}

POST /api/academic/groups/{id}/teaching-assignments
{"subject_id":"…","teacher_id":"…"}
-> 201 {"id":"…","group_id":"…","group_code":"2A","subject_id":"…","subject_code":"MAT1","subject_name":"…",
        "teacher_id":"…","teacher_name":"Nombre Apellido"}

POST /api/academic/teaching-assignments/{id}/schedules
{"day_of_week":1,"start_time":"08:00","end_time":"10:00","classroom":"Aula 3"}
-> 201 {"id":"…","teaching_assignment_id":"…","day_of_week":1,"start_time":"08:00","end_time":"10:00","classroom":"Aula 3"}

POST /api/academic/subject-enrollments
{"student_id":"…","teaching_assignment_id":"…","attempt_number":1}      # attempt_number es opcional
-> 201 {"id":"…","student_id":"…","teaching_assignment_id":"…","attempt_number":1,"status":"ACTIVE","student":{…}}
```
Errores propios: 409 `term_closed`, `invalid_term_transition`, `group_code_taken`, `already_enrolled`, `teaching_assignment_exists`, `schedule_overlap`, `subject_enrollment_exists`, `career_inactive`…; 422 `invalid_dates`, `invalid_level`, `invalid_tutor` / `invalid_teacher` / `invalid_student`, `subject_not_in_curriculum`, `student_not_in_group`, `invalid_attempt_number`, `invalid_day_of_week`, `invalid_time_range`… Una referencia inexistente dentro del cuerpo (p. ej. `career_id`) es **422** (`career_not_found`…), no 404.

**Reglas de Academic**
- Periodo: `draft → active → closed` (sin otras transiciones; `closed` es final). No se exige un único periodo `active`. Nombre único; `start_date < end_date`.
- En un periodo `closed` no se crean ni modifican grupos, tutor, inscripciones, asignaciones, horarios ni inscripciones a materia (409 `term_closed`); la lectura sigue disponible. El periodo se bloquea (`FOR SHARE`) durante cada escritura dependiente, así un cierre concurrente no puede colarse.
- Grupo: `level > 0`, carrera activa, periodo no cerrado, código único por periodo (se guarda en mayúsculas), tutor opcional con rol `TUTOR` activo.
- Asignación docente: materia activa **presente en el plan de la carrera del grupo en el nivel del grupo** (`curriculum_subjects`); profesor con rol `TEACHER` activo; una materia por grupo.
- Horario: `day_of_week` 1–7, formato `HH:MM`, `start_time < end_time`; no se solapan dos franjas de la misma asignación el mismo día.
- Inscripción al grupo: alumno con rol `STUDENT` activo; única por (alumno, grupo).
- Inscripción a materia: el alumno debe tener inscripción activa en el grupo de la asignación; una por (alumno, asignación). `attempt_number` es opcional y, si se envía, debe ser el siguiente intento del alumno en esa materia (1 la primera vez; 2 si ya la cursó en otra asignación, etc.). Es el registro del que colgarán Grades y Attendance.
- Auditoría (`audit_logs`, solo ids, misma transacción que el cambio): `academic.term_created/activated/closed`, `group_created`, `group_tutor_changed`, `student_enrolled`, `teaching_assignment_created`, `teacher_changed`, `subject_enrollment_created`, `subject_enrollments_bulk_created`.

**Decisiones abiertas / propuestas de Academic (no oficiales; confirmar)**
- Alta de catálogos (carreras, materias, unidades, plan) por el DIRECTOR: propuesta para poder poblar el sistema. No hay edición ni baja de catálogos todavía.
- No se impide que un alumno esté en dos grupos del mismo periodo, ni solapes de horario entre asignaciones distintas (mismo profesor o mismo grupo): no están confirmados.
- El intento consecutivo por alumno+materia y los estados `ACTIVE/WITHDRAWN` de `enrollments` y `subject_enrollments` son una propuesta (el diccionario no lista los valores). No existe aún endpoint para dar de baja una inscripción.
- `subject_enrollments` ya no guarda `enrollment_id` (el diccionario enlaza alumno ↔ asignación; migración `0007`); la pertenencia al grupo se valida al crear. `subject_enrollments.created_at` se conserva como extensión documentada del diccionario (auditoría de cuándo se inscribió). Repetir `POST /subject-enrollments` (con o sin `attempt_number`) responde 409 `subject_enrollment_exists` y nunca crea otro intento; un segundo intento legítimo solo puede ser en otra asignación de la misma materia (otro periodo). La inscripción masiva es idempotente.
- Sin paginación: los listados devuelven todo lo visible para el rol.

Errores: `{"error":{"code","message","request_id"}}` — 400 cuerpo inválido, 422 validación, 401, 403, 404, 409, 429 (con `Retry-After`), 500.

## Estructura
```
cmd/{api,migrate,seed}   internal/{app,platform,identity,academic,audit}   migrations/
```
Cada módulo: `domain / application / ports / adapters`.

## Decisiones de seguridad y modelo

**Extensiones al diccionario de datos original** (decididas y conservadas):
- `users.must_change_password` — extensión de seguridad necesaria para implementar de forma segura el reset administrativo. El ADMIN fija una contraseña temporal, la cuenta queda con `must_change_password = true` y solo puede usar `me`, `logout` y cambio de contraseña (el resto responde 403 `password_change_required`) hasta que la cambia; entonces vuelve a `false`. Ni la contraseña ni su hash se devuelven nunca.
- `users.is_active`, `auth_sessions.revoked_reason` y las columnas `late_after_minutes` / `lates_per_absence` de `attendance_sessions` (defaults 15 y 3, ambas enteros > 0; formalizan una regla que el documento marcaba como configurable pero no modelaba).

**Modelo**: `curriculum_subjects` tiene PK compuesta `(career_id, subject_id, level)` y `groups.level` es SMALLINT NOT NULL (migración `0006`). `academic_terms.status` (`draft` por defecto / `active` / `closed`) es la única fuente del estado del periodo; no se impone "un solo periodo `active`" (el modelo oficial solo define draft/active/closed). `groups.tutor_id`: el tutor se asigna al grupo, no por alumno.

**Sesiones**
- Cambiar contraseña exige la contraseña actual, conserva la sesión desde la que se hizo y revoca todas las demás. El reset de ADMIN revoca todas.
- Cada sesión revocada guarda su motivo: `ROTATED`, `LOGOUT`, `PASSWORD_CHANGED`, `PASSWORD_RESET`, `REUSE_DETECTED`.
- Un refresh token revocado siempre da 401. **Solo** un token `ROTATED` presentado después de 10 s (periodo de gracia para reintentos/doble pestaña) se considera reutilización: se revocan las sesiones activas del usuario y se audita `identity.refresh_reuse_detected`. Un token cerrado por logout o cambio de contraseña nunca afecta a otras sesiones.

**Rate limiting de login** (en memoria, por proceso): se limitan los fallos por cuenta+IP (5) y por IP (150, holgado a propósito porque toda la red universitaria puede salir por una misma IP pública/NAT) en 15 min (`LOGIN_*` en `.env.example`), siempre *antes* de consultar al usuario, así que cuentas existentes e inexistentes se comportan igual (429 + `Retry-After`). La cuenta se combina con la IP para que un tercero no pueda bloquear a la víctima. No almacena contraseñas. Con más de una instancia habría que sustituirlo (puerto `ports.LoginLimiter`).

**Auditoría actual** (`audit_logs`, sin secretos): `identity.user_created`, `identity.password_changed`, `identity.password_reset`, `identity.logout`, `identity.refresh_reuse_detected`. No se audita el login exitoso (cada sesión ya queda en `auth_sessions` con IP y user-agent, y sería mucho volumen) ni el login fallido (lo cubren el rate limit y el log de acceso).

## Limitaciones conocidas
- El rate limiting es por proceso; no protege contra un ataque distribuido sobre una cuenta desde muchas IPs.
- Migraciones solo hacia adelante.
- El workflow de CI está escrito pero **no se ha ejecutado en GitHub**.
- No hay endpoint de consulta de auditoría.
- Datos de prueba: solo ficticios (matrículas tipo `10000001`).
