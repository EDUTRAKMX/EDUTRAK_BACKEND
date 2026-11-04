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
| Grades (captura y corrección de calificaciones, consulta por alumno y por asignación docente) | Implementado (`/api/grades`, `/api/students/{id}/grades`, `/api/teaching-assignments/{id}/grades`) |
| Attendance (sesiones, QR dinámico con red institucional, captura manual, retardos, cierre, consultas) | Implementado (`/api/attendance/*`); NFC solo previsto como método |
| Justifications (solicitud con evidencias privadas, revisión Tutor → Profesor, JUSTIFIED atómico en Attendance, consultas) | Implementado (`/api/justifications/*`) |
| Tutoring (alumnos del tutor, resumen académico, seguimientos append-only, historial) | Implementado (`/api/tutoring/*`); sin pendientes/estados (el esquema no los tiene) |
| Analytics (indicadores por alumno/grupo, resumen del director, alertas explicables con recálculo idempotente) | Implementado (`/api/analytics/*`); sin predicción ni puntaje de riesgo |

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
`DATABASE_URL` y `JWT_SECRET` son obligatorias (el arranque falla sin ellas). Opcionales: `HTTP_PORT` (8080), `ACCESS_TOKEN_TTL` (15m), `REFRESH_TOKEN_TTL` (168h), `QR_TOKEN_TTL` (30s), `LOGIN_MAX_FAILURES_PER_ACCOUNT` (5), `LOGIN_MAX_FAILURES_PER_IP` (150), `LOGIN_RATE_WINDOW` (15m), `ALLOWED_NETWORKS` (obligatoria en producción), `TRUSTED_PROXIES`, `ATTENDANCE_ALLOW_ANY_NETWORK` (solo dev), `JUSTIFICATION_EVIDENCE_REQUIRED`, `ATTENDANCE_LATE_AFTER_MINUTES`, `ATTENDANCE_LATES_PER_ABSENCE`, `INSTITUTION_TIMEZONE`, `STORAGE_DIR`, `MAX_EVIDENCE_MB`, `APP_ENV`, `LOG_LEVEL`. Ver `.env.example`. Un archivo `.env` se carga si existe sin pisar variables ya definidas.

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

### Grades
Todas las rutas exigen `Authorization: Bearer <access_token>`. La identidad del profesor sale **solo** del token; un `teacher_id` / `recorded_by` en el cuerpo se rechaza (400, campo desconocido). Las listas y detalles respetan **rol + relación con el recurso**; un `ADMIN` no obtiene expedientes por serlo (403).

| Método | Ruta | Acceso |
|---|---|---|
| POST | `/api/grades` | TEACHER **responsable** de la asignación de esa inscripción (nadie más: ni DIRECTOR, TUTOR, STUDENT ni ADMIN) |
| PATCH | `/api/grades/{id}` | el mismo profesor responsable (corrección) |
| GET | `/api/grades/{id}` | el alumno dueño, el profesor de la asignación, el tutor del grupo, DIRECTOR |
| GET | `/api/students/{id}/grades` | STUDENT: solo `id` propio · TEACHER: solo intentos de sus asignaciones · TUTOR: solo intentos de grupos que tutela · DIRECTOR: cualquier alumno · ADMIN: 403 |
| GET | `/api/teaching-assignments/{id}/grades` | profesor de la asignación, tutor del grupo, DIRECTOR |

```http
POST /api/grades
{"subject_enrollment_id":"…","subject_unit_id":"…","score":89.99}
-> 201 {"id":"…","subject_enrollment_id":"…","subject_unit_id":"…","score":89.99,"category":"SA",
        "recorded_by":"<profesor autenticado>","recorded_at":"…","updated_at":"…"}

PATCH /api/grades/{id}        {"score":90}   -> 200 (mismo objeto; "score":90.00,"category":"DE")

GET /api/students/{id}/grades
-> 200 {"student_id":"…","data":[{"subject_enrollment_id":"…","teaching_assignment_id":"…","attempt_number":1,
        "status":"ACTIVE","academic_term":{"id":"…","name":"2026-1"},"group_code":"2A",
        "subject":{"id":"…","code":"MAT1","name":"…"},
        "units":[{"subject_unit_id":"…","unit_number":1,"unit_name":"…",
                  "grade":{"id":"…","score":0.00,"category":"NA","updated_at":"…"}},
                 {"subject_unit_id":"…","unit_number":2,"unit_name":"…","grade":null}]}]}

GET /api/teaching-assignments/{id}/grades
-> 200 {"teaching_assignment_id":"…","group_id":"…","group_code":"2A","subject":{…},
        "units":[{"id":"…","unit_number":1,"name":"…"},…],
        "students":[{"subject_enrollment_id":"…","student":{"id":"…","institutional_number":"…","first_name":"…","last_name":"…"},
                     "attempt_number":1,"status":"ACTIVE","units":[ /* una celda por unidad, mismo formato que arriba */ ]}]}
```
Una unidad **sin calificación** devuelve `"grade": null`; **nunca** se trata como 0.00 (un 0.00 capturado sí aparece como calificación). No se calculan promedios (ver decisiones abiertas).

Errores: 400 cuerpo mal formado o campo desconocido (`invalid_body`) · 401 · 403 `forbidden` · 404 `grade_not_found`, `student_not_found`, `teaching_assignment_not_found` · 409 `grade_exists`, `term_closed`, `subject_enrollment_inactive` · 422 `invalid_score`, `invalid_score_precision`, `score_out_of_range`, `subject_enrollment_not_found`, `subject_unit_not_found`, `unit_not_in_subject`.

**Reglas de Grades**
- Escala 0–100 con a lo sumo dos decimales; se guarda exactamente como `NUMERIC(5,2)` (sin `float`: el dominio usa **centésimos enteros** y la API serializa el número con dos decimales exactos). Más de dos decimales significativos (`89.999`) se **rechaza**, nunca se redondea; ceros sobrantes (`90.000`) son el mismo valor. Se rechazan signo, exponente (`1e2`), `+`, comas y cualquier valor fuera de 0–100.
- Categoría derivada del score exacto (no se almacena): `<80.00` NA · `80.00–89.99` SA · `90.00–99.99` DE · `100.00` AU. Fronteras probadas: 0.00 NA, 79.99 NA, 80.00 SA, 89.99 SA, 90.00 DE, 99.99 DE, 100.00 AU; −0.01 y 100.01 → 422.
- Una calificación = un intento (`subject_enrollment`) + una unidad. `UNIQUE (subject_enrollment_id, subject_unit_id)` en PostgreSQL: un POST repetido o simultáneo → 409 y **no** sobrescribe; corregir se hace con PATCH. Los intentos de distintos periodos son inscripciones a materia distintas, así que no se mezclan.
- Para capturar/corregir: la unidad pertenece a la materia de la asignación, la inscripción está `ACTIVE`, el periodo no está `closed` (409 `term_closed`; el periodo se bloquea `FOR SHARE` durante la escritura) y el profesor autenticado es el de la asignación **en ese momento** (si Academic cambia al profesor, el anterior pierde el acceso).
- PATCH con el mismo valor es un no-op (sin escritura ni auditoría). `recorded_by` conserva a quien capturó; quién corrigió queda en la auditoría. Las correcciones concurrentes se serializan con bloqueo de fila (sin pérdida de actualizaciones).
- Auditoría (misma transacción que el cambio, solo ids y valores): `grades.created` (`subject_enrollment_id`, `subject_unit_id`, `score`, `category`) y `grades.updated` (además `from`, `to`, `from_category`, `to_category`); el actor es el profesor autenticado.
- Consultas sin N+1: la hoja de un alumno es 1 consulta; la tabla de una asignación son 3 consultas sin importar el tamaño del grupo.

**Relación Grades ↔ Academic.** Grades no duplica reglas de Academic: lee `subject_enrollments`, `teaching_assignments`, `groups`, `academic_terms` y `subject_units` mediante el puerto `ports.Academic` (adaptador de solo lectura) y confía en que Academic ya validó que el alumno pertenece al grupo y la unidad a la materia del catálogo.

**Migración `0008`**: renombra `grades.captured_by → recorded_by` y `created_at → recorded_at` (diccionario). `score NUMERIC(5,2)`, `CHECK 0–100` y `UNIQUE` ya existían desde `0003`; se probó BD vacía y upgrade con datos.

**Decisiones abiertas de Grades**
- **Promedios / calificación final por materia:** no se calculan ni se exponen (no hay política de ponderación confirmada; las unidades pendientes no cuentan como cero).
- Quién puede capturar: solo el profesor responsable. Si el DIRECTOR (u otro rol) debe poder corregir, hay que confirmarlo.
- Un profesor puede corregir sin límite de tiempo mientras el periodo no esté cerrado (no hay política de bloqueo por fechas documentada).
- No existe borrado de calificaciones; una captura equivocada se corrige con PATCH.
- El tutor puede ver la tabla completa de la asignación de su grupo, no solo sus tutorados; con grupos de un solo tutor es equivalente, confirmar si debe acotarse.

### Attendance (`/api/attendance`)
Todas las rutas exigen `Authorization: Bearer <access_token>`. La autorización es **rol + relación con el recurso** y se decide en los casos de uso; el alumno que registra es siempre el del token (cualquier `student_id`, `ip`, hora o `status` en el cuerpo del check-in → 400). ADMIN no obtiene asistencia por serlo (403). Rutas relativas a `/api/attendance`.

| Método | Ruta | Acceso |
|---|---|---|
| POST | `/sessions` `{"teaching_assignment_id","schedule_id"?,"late_after_minutes"?,"lates_per_absence"?}` | TEACHER **responsable** de la asignación |
| GET | `/sessions/{id}` | profesor de la asignación, tutor del grupo, DIRECTOR |
| POST | `/sessions/{id}/qr/rotate` | profesor responsable (sesión OPEN) |
| POST | `/sessions/{id}/check-in` `{"token"}` | STUDENT inscrito en la asignación, desde la red institucional |
| POST | `/sessions/{id}/manual` `{"subject_enrollment_id","status"}` | profesor responsable (sesión OPEN) |
| PATCH | `/records/{id}` `{"status"}` | profesor responsable |
| POST | `/sessions/{id}/close` | profesor responsable |
| GET | `/teaching-assignments/{id}` | profesor de la asignación, tutor del grupo, DIRECTOR |
| GET | `/students/{id}` | STUDENT: solo el propio · TEACHER: solo intentos de sus asignaciones · TUTOR: solo grupos que tutela · DIRECTOR: cualquiera |

```http
POST /api/attendance/sessions        {"teaching_assignment_id":"…"}
-> 201 {"id":"…","teaching_assignment_id":"…","schedule_id":null,"session_date":"2026-02-02","status":"OPEN",
        "opened_by":"…","opened_at":"…","closed_at":null,"late_after_minutes":15,"lates_per_absence":3}

POST /api/attendance/sessions/{id}/qr/rotate
-> 200 {"attendance_session_id":"…","token":"<43 caracteres base64url>","expires_at":"…"}     (Cache-Control: no-store)

POST /api/attendance/sessions/{id}/check-in   {"token":"<token escaneado>"}
-> 201 {"id":"…","attendance_session_id":"…","subject_enrollment_id":"…","status":"PRESENT|LATE","method":"QR",
        "recorded_at":"…","recorded_by":"<alumno autenticado>"}

POST /api/attendance/sessions/{id}/manual   {"subject_enrollment_id":"…","status":"PRESENT|LATE|ABSENT"}   -> 201 (method MANUAL)
PATCH /api/attendance/records/{id}          {"status":"PRESENT|LATE|ABSENT"}                              -> 200
POST /api/attendance/sessions/{id}/close    -> 200 {"session":{…,"status":"CLOSED"},"absences_created":3}

GET /api/attendance/sessions/{id}
-> 200 {"session":{…},"counts":{"present":1,"late":0,"absent":0,"justified":0},"pending":1,
        "students":[{"subject_enrollment_id":"…","student":{…},"record":{…} | null}]}

GET /api/attendance/teaching-assignments/{id}
-> 200 {"teaching_assignment_id":"…","total_sessions":3,"sessions":[{…,"counts":{…}}],
        "students":[{"subject_enrollment_id":"…","student":{…},"status":"ACTIVE",
                     "attendance":{"present":1,"late":3,"absent":0,"justified":0,
                                   "lates_per_absence":3,"absences_from_lates":1,"effective_absences":1}}]}

GET /api/attendance/students/{id}
-> 200 {"student_id":"…","data":[{"subject_enrollment_id":"…","teaching_assignment_id":"…","attempt_number":1,
        "academic_term":{…},"group_code":"2A","subject":{…},"attendance":{…como arriba…},
        "records":[{"id":"…","attendance_session_id":"…","session_date":"2026-02-02","status":"LATE","method":"QR","recorded_at":"…"}]}]}
```
Errores: 400 `invalid_body` · 401 · 403 `forbidden`, `network_not_allowed`, `not_enrolled`, `invalid_qr_token`, `qr_expired` · 404 `attendance_session_not_found`, `attendance_record_not_found`, `teaching_assignment_not_found`, `student_not_found` · 409 `session_already_open`, `session_closed`, `attendance_already_registered`, `term_closed`, `subject_enrollment_inactive`, `record_justified` · 422 `qr_token_required`, `status_not_allowed`, `invalid_late_after_minutes`, `invalid_lates_per_absence`, `subject_enrollment_not_found`, `schedule_not_in_assignment`, `teaching_assignment_not_found`.

**Flujo QR.** (1) El profesor abre la sesión (`POST /sessions`). (2) La pantalla del profesor llama a `qr/rotate` cada ≈`QR_TOKEN_TTL` (30 s por defecto) y dibuja un QR con `{session_id, token}` (el backend no genera imágenes). (3) El alumno escanea y envía `{token}`. Cada rotación **invalida el token anterior**; el token es de 256 bits (`crypto/rand`), se devuelve una sola vez y en la base solo se guarda su SHA-256 (`qr_token_hash`) con `qr_expires_at`; un token vencido o rotado no registra nada, así que una captura de pantalla vieja deja de servir. El token vale para todos los alumnos mientras esté vigente (es el QR proyectado en clase). Cerrar la sesión borra el QR. Las rotaciones no se auditan una por una (sería ruido cada 30 s); el token nunca se guarda en auditoría ni en logs.

**Validación de red (solo para el check-in por QR).** La dirección sale de `netx.ClientIP`: la conexión TCP real, o la cadena `X-Forwarded-For` **únicamente** si el peer está en `TRUSTED_PROXIES` (se recorre de derecha a izquierda, así que entradas antepuestas por el cliente no cuentan). Nunca se lee del cuerpo, de parámetros ni de otros encabezados. Debe pertenecer a `ALLOWED_NETWORKS` (CIDR/IP, sin valores por defecto). Reglas de seguridad:
- Sin `ALLOWED_NETWORKS` **no se acepta ningún check-in** (jamás significa "cualquier red"); ni siquiera `10.x`/`192.168.x` cuentan por sí solos como prueba de presencia.
- `APP_ENV=production` **no arranca** sin `ALLOWED_NETWORKS`, con `0.0.0.0/0` o `::/0`, ni con `ATTENDANCE_ALLOW_ANY_NETWORK=true`.
- `ATTENDANCE_ALLOW_ANY_NETWORK=true` existe solo para desarrollo/pruebas; en producción se ignora aunque llegara a la configuración.
- Si el backend está en Internet, los alumnos de la universidad suelen compartir una **IP pública de salida (NAT)**: configura esa dirección real (consúltala con redes). Si hay proxy inverso, ponlo en `TRUSTED_PROXIES`; si no, todos los clientes parecerán venir del proxy.
- La red es una señal adicional, **no prueba absoluta de presencia física** (cualquiera dentro de la red con el QR puede registrarse). No se implementa ningún "ping" del navegador. La captura manual del profesor no depende de la red ni del QR.
- Variables: `ALLOWED_NETWORKS`, `TRUSTED_PROXIES`, `QR_TOKEN_TTL` (5 s–5 m), `ATTENDANCE_ALLOW_ANY_NETWORK`, `ATTENDANCE_LATE_AFTER_MINUTES` (15), `ATTENDANCE_LATES_PER_ABSENCE` (3), `INSTITUTION_TIMEZONE` (`America/Mexico_City`). El ejemplo de `.env.example` usa `192.0.2.0/24`, un rango de documentación **ficticio**.

**Reglas de asistencia**
- Una sesión pertenece a una asignación docente y está `OPEN` o `CLOSED`. Solo puede haber **una sesión OPEN por asignación** (índice único parcial en PostgreSQL, migración `0009`); tras cerrarla se puede abrir otra. `session_date` es la fecha local en `INSTITUTION_TIMEZONE`.
- **Puntualidad (criterio elegido: la apertura de la sesión, no el horario programado, que es opcional).** Check-in antes de `opened_at + late_after_minutes` → `PRESENT`; desde ese instante exacto → `LATE`. Se calcula con la hora del **servidor**; el cliente no envía hora. Ambos parámetros (15 y 3 por defecto) se copian a la sesión y el profesor puede cambiarlos al abrirla (1–1440 min; 1–100 retardos).
- **3 retardos = 1 falta es un indicador, no una reescritura:** los registros `LATE` nunca se convierten en `ABSENT`. La API devuelve `absences_from_lates = late / lates_per_absence` (división entera) y `effective_absences = absent + absences_from_lates`; `JUSTIFIED` no cuenta. Se usa el `lates_per_absence` de la sesión más reciente.
- Un registro por alumno y sesión (`UNIQUE` en PostgreSQL): el segundo intento, simultáneo o no, da 409 sin tocar el primero. El alumno debe tener inscripción a materia `ACTIVE` en la asignación.
- **Captura manual:** el profesor responsable fija `PRESENT`, `LATE` o `ABSENT` (método `MANUAL`) con la sesión OPEN; **`JUSTIFIED` nunca** se acepta (corresponde al flujo de Justifications). NFC está previsto en el esquema como método, pero **no está implementado**.
- **Corrección (política conservadora, no definida formalmente):** solo el profesor responsable, con el periodo abierto, sobre sesiones abiertas **o cerradas**, entre `PRESENT/LATE/ABSENT`; un registro `JUSTIFIED` no se puede tocar (409). El registro conserva `method` y `recorded_by` originales; el cambio queda en `audit_logs` (`from`, `to`, estado de la sesión). Un valor igual es un no-op.
- **Cierre:** en una transacción, con la sesión bloqueada en exclusiva (ningún check-in se intercala), se crean registros `ABSENT` (método `MANUAL`, `recorded_by` = quien cierra) para cada inscripción a materia `ACTIVE` sin registro, se borra el QR y la sesión queda `CLOSED`. Es idempotente (nunca toca registros existentes) y un segundo cierre responde 409. Se generan las ausencias porque Justifications necesita un registro `ABSENT` al que referirse.
- Con el periodo `closed` ninguna escritura de asistencia es posible (409 `term_closed`); la consulta sigue disponible.
- Auditoría (misma transacción, solo ids): `attendance.session_started`, `attendance.recorded` (QR y manual), `attendance.corrected`, `attendance.session_closed`.
- Concurrencia: check-in y captura toman la sesión `FOR SHARE`; rotación y cierre `FOR UPDATE`; el periodo se bloquea `FOR SHARE`. Un check-in y un cierre simultáneos terminan con un solo registro coherente por alumno.
- Consultas sin N+1: detalle de sesión 2 consultas, resumen de asignación 2, historial de alumno 1.

**Migración `0009`**: índice único parcial (una sesión `OPEN` por asignación) y dos `CHECK` de coherencia del QR (hash y vencimiento van juntos; una sesión cerrada no guarda QR). Probada en BD vacía y como upgrade con sesiones y registros previos. Si una base ya tuviera dos sesiones `OPEN` de la misma asignación, la migración falla (no corrige datos en silencio); hoy no existen porque Attendance es nuevo.

**Decisiones abiertas de Attendance**
- Referencia de puntualidad: apertura de la sesión (elegida) vs hora de inicio del horario.
- Ventana de tiempo para corregir sesiones cerradas (hoy sin límite mientras el periodo esté abierto).
- Una sola sesión OPEN por asignación: propuesta; confirmar si hay clases de varios bloques que requieran más.
- Zona horaria de la institución (`America/Mexico_City` por defecto: supuesto) y rangos reales de red/NAT de la universidad.
- ABSENT generado al cerrar usa `method = MANUAL` (el esquema exige un método); alternativa: columna de origen en una migración futura.
- El token QR es válido para todos los alumnos durante su vigencia; si se quisiera de un solo uso por alumno habría que cambiar el diseño.
- No hay endpoint de listado de sesiones por fecha/profesor ni de reinscripción de un registro `JUSTIFIED`; llegarán con Justifications.

### Justifications (`/api/justifications`)
Todas las rutas exigen `Authorization: Bearer <access_token>`. La autorización es **rol + relación vigente con el recurso**, releída en cada llamada (si cambian el tutor del grupo o el profesor de la asignación, el acceso cambia de inmediato). ADMIN no obtiene solicitudes ni evidencias por serlo (403). Rutas relativas a `/api/justifications`.

| Método | Ruta | Acceso |
|---|---|---|
| POST | `` (JSON o `multipart/form-data`) | STUDENT, solo para una ausencia propia |
| GET | `?status=` | lista filtrada en el backend: STUDENT las suyas · TUTOR las de alumnos de sus grupos · TEACHER las de sus asignaciones · DIRECTOR todas |
| GET | `/{id}` | el alumno dueño, el tutor del grupo, el profesor de la asignación, DIRECTOR |
| POST | `/{id}/tutor-decision` `{"decision":"APPROVED\|REJECTED","comment"}` | TUTOR **actual** del grupo del alumno |
| POST | `/{id}/teacher-decision` `{"decision","comment"}` | TEACHER de la asignación de la ausencia |
| GET | `/{id}/evidence/{evidenceId}` | los mismos que el detalle |

**Solicitud.** `Content-Type: application/json` (sin evidencia) → `{"attendance_record_id":"…","reason":"…"}`. Con evidencia: `multipart/form-data` con los campos de texto `attendance_record_id` y `reason` **antes** de los archivos y uno o más archivos en el campo `evidence` (máx. 5). Se lee en streaming; nada de base64.
```http
POST /api/justifications        Content-Type: multipart/form-data; boundary=…
  attendance_record_id=<uuid>   reason=Consulta médica   evidence=@constancia.pdf   evidence=@receta.jpg
-> 201 {"id":"…","attendance_record_id":"…","teaching_assignment_id":"…","status":"PENDING_TUTOR","awaiting_stage":"TUTOR",
        "reason":"Consulta médica","student":{…},"absence":{"session_date":"2026-02-02","attendance_status":"ABSENT",
        "group_code":"2A","subject":{"code":"MAT1","name":"…"},"academic_term":"2026-1"},
        "evidence_count":2,"created_at":"…","updated_at":"…",
        "evidence":[{"id":"…","original_filename":"constancia.pdf","mime_type":"application/pdf","size_bytes":1234,"created_at":"…"}],
        "reviews":[]}

POST /api/justifications/{id}/tutor-decision    {"decision":"REJECTED","comment":"Sin comprobante"}   -> 200 (mismo detalle, reviews con la decisión)
GET  /api/justifications/{id}/evidence/{evId}   -> 200 <bytes>  Content-Type: application/pdf | image/jpeg | image/png
        Content-Disposition: attachment; filename=…   X-Content-Type-Options: nosniff   Cache-Control: private, no-store
```
Una revisión devuelve `{"id","stage":"TUTOR|TEACHER","decision":"APPROVED|REJECTED","comment","reviewer":{"id","name"},"reviewed_at"}`. **Nunca** se devuelven `storage_key` ni rutas.

Errores: 400 `invalid_body` (JSON/multipart mal formado, campos desconocidos, `student_id` en el cuerpo) · 401 · 403 `forbidden` · 404 `justification_not_found`, `evidence_not_found` · 409 `justification_exists`, `absence_not_eligible`, `wrong_review_stage`, `justification_finalized`, `term_closed`, `subject_enrollment_inactive` · **413** `evidence_too_large` · **415** `unsupported_evidence_type`, `evidence_type_mismatch`, `unsupported_content_type` · 422 `reason_required`, `reason_too_long`, `invalid_decision`, `comment_required`, `evidence_required`, `too_many_files`, `empty_evidence`, `invalid_filename`, `fields_after_files`, `invalid_attendance_record_id`, `attendance_record_not_found`, `invalid_status`.

**Máquina de estados** (lógica explícita en `domain`, tabla de transiciones):
```
PENDING_TUTOR   --Tutor APPROVED--> PENDING_TEACHER --Profesor APPROVED--> JUSTIFIED         (final)
PENDING_TUTOR   --Tutor REJECTED--> TUTOR_REJECTED                                           (final)
                                    PENDING_TEACHER --Profesor REJECTED--> TEACHER_REJECTED  (final)
```
El profesor no puede decidir antes que el tutor, el tutor no puede dar la decisión final, una etapa no se decide dos veces y una solicitud finalizada es inmutable (reapertura formal fuera del MVP). Cada decisión se agrega a `justification_reviews` (historial **append-only**: no se sobrescribe la decisión del tutor con la del profesor; un rechazo no borra nada). Rechazar exige comentario; aprobar lo admite opcional. Tras un rechazo el alumno puede crear una **nueva** solicitud para la misma ausencia (el índice único parcial solo cuenta solicitudes abiertas o aprobadas).

**Elegibilidad.** Solo una asistencia `ABSENT` propia, de una inscripción a materia `ACTIVE` y de un periodo no cerrado. `PRESENT`, `LATE` y `JUSTIFIED` no son justificables (no hay regla institucional para los retardos). El modelo tiene **un solo campo de texto** (`reason`, 1–2000 caracteres); no hay "motivo" y "descripción" separados. **No se define ningún plazo** (24/48/72 h): pendiente de la institución. La evidencia es **opcional** salvo `JUSTIFICATION_EVIDENCE_REQUIRED=true` (la institución no lo ha confirmado).

**Integración atómica con Attendance.** Attendance sigue sin aceptar `JUSTIFIED` en sus endpoints públicos (manual y PATCH). La única vía es la operación **interna** `JustifyAbsence` de su capa de aplicación (sin endpoint), invocada desde Justifications a través de un puerto (`adapters/attendance`); Justifications nunca escribe tablas de Attendance. La decisión final favorable ejecuta, en **una** transacción PostgreSQL: estado de la solicitud → `JUSTIFIED`, revisión del profesor, `attendance_records.status` `ABSENT → JUSTIFIED` (con `UPDATE … WHERE status = 'ABSENT'`) y los eventos de auditoría. Si algo falla, todo se revierte; nunca queda una solicitud `JUSTIFIED` con la asistencia en `ABSENT` ni al revés. Si la ausencia dejó de ser `ABSENT` (p. ej. el profesor la corrigió a `PRESENT`), la **aprobación falla con 409 `absence_not_eligible`** y no se fuerza nada (el rechazo sigue siendo posible para cerrar la solicitud). Orden de bloqueos en todo el flujo: (1) fila de `attendance_records` `FOR UPDATE`, (2) fila de `justification_requests` `FOR UPDATE`; Attendance bloquea sesión y luego registro, y Justifications nunca toma la sesión, así que no hay ciclos. El bloqueo ordena las operaciones; la garantía de consistencia no depende solo de él, porque el `UPDATE` condicional es una segunda barrera.

**Evidencias privadas (seguridad).**
- Tipos: PDF, JPG/JPEG y PNG; máximo `MAX_EVIDENCE_MB` (≤ 10 MiB) por archivo, hasta 5 por solicitud. Se valida por **contenido** (firmas `%PDF-`, `FFD8FF`, `89504E47…`), por extensión y que ambas coincidan; el `Content-Type` del cliente se ignora. Ejecutables, HTML, ZIP, GIF, etc. → 415. El tamaño se corta durante la lectura (se leen a lo sumo `max+1` bytes del cliente; el cuerpo total de la petición también está acotado).
- El nombre del cliente solo se usa para mostrar (se le quitan rutas y caracteres de control). La **clave de almacenamiento** es aleatoria (`<2 hex>/<32 hex>.<pdf|jpg|png>`) y su extensión sale del contenido, nunca del nombre; el adaptador rechaza cualquier clave con otra forma (sin `..`, separadores ni rutas absolutas), crea con `O_EXCL` (nunca sobrescribe), permisos `0700/0600`.
- Almacenamiento: puerto `EvidenceStorage`; adaptador de filesystem en `STORAGE_DIR` (por defecto `./data/evidence`, ignorado por git). **Nunca** debe estar bajo una carpeta pública ni servida por NGINX; los archivos solo salen por `GET …/evidence/{id}`, que re-autoriza cada vez y responde con el tipo inspeccionado, `attachment`, `nosniff`, `no-store` y una CSP restrictiva. En producción `STORAGE_DIR` debe ser una ruta absoluta explícita (si no, el arranque falla). Cambiar a object storage = otra implementación del puerto.
- **Limitaciones:** no hay antivirus ni análisis de malware (solo se comprueba el tipo); una firma válida no garantiza un documento inofensivo. No se cifra en reposo ni se hace respaldo desde la aplicación: respalda `STORAGE_DIR` junto con la base. Los archivos no son recuperables por URL.

**Consistencia BD ↔ filesystem (no es atómica y no se afirma que lo sea).** Orden: (1) validar todo lo posible sin efectos laterales, (2) escribir los archivos en el almacenamiento privado con claves aleatorias que nada referencia aún, (3) **una** transacción de BD que revalida bajo bloqueo e inserta solicitud, metadatos de evidencia y auditoría, (4) si algo falla (validación, BD, auditoría, archivo posterior, cancelación), se borran los archivos escritos. Fallos de limpieza se registran (solo la clave, nunca contenido) y no tapan el error original. Si el proceso se interrumpe entre (2) y (3) quedan archivos **huérfanos** sin referencia (inofensivos, invisibles por la API); nunca hay una fila que apunte a un archivo inexistente. Pendiente: tarea de barrido que compare `storage_key` con el directorio. Una solicitud con metadatos sin archivo (borrado manual) responde 500 sin detalles y queda en el log.

**Auditoría** (misma transacción que el cambio; solo ids, estados y decisión, sin archivos ni rutas): `justifications.requested` (`attendance_record_id`, `evidence_count`), `justifications.tutor_approved|tutor_rejected|teacher_approved|teacher_rejected` (`stage`, `decision`, `from`, `to`) y `attendance.justified` (entidad: el registro de asistencia).

**Migración `0010`**: `UNIQUE (justification_request_id, stage)` en revisiones, trigger que impide `UPDATE`/`DELETE` del historial, `CHECK` de comentario obligatorio al rechazar y de motivo no vacío (cualquier espacio en blanco cuenta como vacío) e índice por `attendance_record_id`. Las columnas de las tres tablas ya coincidían con el modelo. `reviewed_at` se escribe con `clock_timestamp()` para que el orden del historial refleje el orden real de las decisiones. Probada en BD vacía y como upgrade con solicitud, evidencia y revisión previas.

**Decisiones abiertas de Justifications**
- Plazo máximo para solicitar (sin definir; no se aplica ninguno) y si el `LATE` puede justificarse.
- Si la evidencia es obligatoria (hoy opcional, configurable) y el límite de 5 archivos (propuesta).
- Qué hacer con una solicitud abierta cuando la asistencia se corrige a `PRESENT`: hoy no se cancela sola; el tutor/profesor puede rechazarla.
- Reapertura formal, apelaciones, notificaciones y barrido de huérfanos: fuera del MVP.
- El DIRECTOR solo consulta (no decide); confirmar si debe poder intervenir.

### Tutoring (`/api/tutoring`)
Seguimiento tutorial: el tutor consulta a los alumnos de sus grupos, ve su situación académica (leída de Academic, Grades, Attendance y Justifications; **nada se copia** a Tutoring) y registra seguimientos. Todas las rutas exigen `Authorization: Bearer <access_token>`. La autorización es **rol + relación vigente**: el TUTOR solo opera sobre grupos donde `groups.tutor_id` es él **ahora** (se relee en cada llamada; si el grupo cambia de tutor, el anterior pierde el acceso al instante y el nuevo hereda el historial). El rol TUTOR por sí solo no basta. ADMIN, TEACHER y STUDENT reciben 403 en todo; DIRECTOR solo consulta y supervisa (no escribe seguimientos: no hay una regla que lo autorice).

| Método | Ruta | Acceso |
|---|---|---|
| GET | `/students?academic_term_id=&group_id=&status=` | TUTOR: alumnos de sus grupos · DIRECTOR: todos. `status` = `ACTIVE` (por defecto) o `WITHDRAWN` |
| GET | `/students/{id}?group_id=` | resumen académico de UN alumno en UN grupo (sin `group_id`: la inscripción más reciente a la que el llamante tiene acceso) |
| GET | `/students/{id}/follow-ups?group_id=&limit=&offset=` | historial del alumno, más reciente primero |
| POST | `/follow-ups` | solo el TUTOR actual del grupo |
| GET | `/follow-ups?student_id=&group_id=&academic_term_id=&from=&to=&limit=&offset=` | seguimientos dentro del alcance del llamante |
| GET | `/follow-ups/{id}` | tutor actual del grupo · DIRECTOR |

```http
POST /api/tutoring/follow-ups
{"student_id":"<uuid>","group_id":"<uuid>","followup_date":"2026-02-10","note":"Acordamos revisar Matemáticas"}
-> 201 {"id","student":{"id","institutional_number","first_name","last_name"},"group":{"id","code"},
        "academic_term":{"id","name"},"author":{"id","name"},"followup_date":"2026-02-10","note":"…","created_at":"…"}

GET /api/tutoring/students/{id}/follow-ups?limit=50&offset=0
-> 200 {"data":[<seguimiento>…],"total":3,"limit":50,"offset":0}

GET /api/tutoring/students -> 200 {"data":[{"student":{…},"group":{"id","code","level","tutor_id"},"career":{"code","name"},
        "academic_term":{"id","name","status","start_date","end_date"},"enrollment_status","followups_count","last_followup_date"}]}

GET /api/tutoring/students/{id}?group_id=…
-> 200 {"enrollment":{…como arriba…},"subjects":[{"subject_enrollment_id","teaching_assignment_id","subject":{"code","name"},"teacher":{"name"},
        "attempt_number","status",
        "grades":{"units_total":7,"units_graded":2,"units":[{"subject_unit_id","unit_number","unit_name","grade":{"score":90.00,"category":"DE"}|null}]},
        "attendance":{"present","late","absent","justified","total","lates_per_absence","absences_from_lates","effective_absences"},
        "justifications":{"pending","approved","rejected"}}]}
```
- `followup_date` es opcional (por defecto, hoy en la zona horaria institucional). Debe estar entre el inicio del periodo del grupo y hoy; no puede ser futura.
- `author` sale del token, **nunca** del cuerpo (cualquier campo desconocido, como un `tutor_id`, da 400) y se conserva aunque el grupo cambie de tutor.
- Resumen académico: `grade: null` = unidad sin capturar (**no** es 0.00; un 0.00 capturado sí aparece con categoría `NA`). Categorías NA/SA/DE/AU con las reglas de Grades. `absences_from_lates` y `effective_absences` siguen la regla de Attendance (N retardos = 1 falta, solo indicador; N sale de la sesión abierta más recientemente; sin sesiones, `lates_per_absence` es `null` y no se deriva nada). **No hay promedios, calificación final, puntaje de riesgo, predicciones ni alertas**: eso es Analytics y la institución no ha definido criterios.
- Dos consultas SQL constantes para el resumen (materias+asistencia+justificaciones, y unidades+calificaciones) y una para la lista de alumnos con contadores, sin N+1.

Errores: 400 `invalid_body` (JSON mal formado o campos desconocidos) · 401 · 403 `forbidden` · 404 `student_not_found`, `followup_not_found`, `enrollment_not_found` (el alumno no está inscrito en el `group_id` de la URL) · 409 `term_closed`, `enrollment_inactive` · 422 `note_required`, `note_too_long` (máx. 4000 caracteres), `invalid_followup_date`, `followup_date_in_future`, `followup_date_before_term`, `invalid_student_id`, `invalid_group_id`, `student_not_found` / `group_not_found` / `student_not_enrolled` (referencias del cuerpo), `invalid_filter`, `invalid_paging` (`limit` 1–200, `offset` ≥ 0).

**Reglas.**
- Un seguimiento pertenece a una inscripción concreta (alumno + grupo, y por tanto periodo). Un alumno con inscripciones en varios grupos/periodos tiene historiales **separados**; el `group_id` los distingue y nunca se mezclan. El TUTOR ve los seguimientos de los grupos que tutela hoy, sin importar quién los escribió.
- Escribir exige: rol TUTOR + ser el tutor actual del grupo + inscripción `ACTIVE` + periodo no cerrado (el grupo y su periodo se bloquean `FOR SHARE` durante la transacción: un cambio de tutor o el cierre del periodo concurrente no puede colarse entre la comprobación y la escritura).
- El historial es **append-only**: cada intervención es una fila nueva; no hay edición ni borrado (un trigger lo impide también en SQL). No se crean restricciones de unicidad: varios seguimientos del mismo alumno el mismo día son legítimos.
- **Privacidad.** El texto (`note`) solo se devuelve a quien tiene relación vigente. Nunca va a logs ni a la auditoría, ni aparece en errores. No lo uses para diagnósticos médicos/psicológicos: debe limitarse al acompañamiento académico (es una regla de uso, el sistema no puede verificarla).
- **Auditoría** (misma transacción; si falla, no se guarda el seguimiento): `tutoring.followup_created` con `student_id`, `group_id` y `followup_date` (sin texto). Las lecturas no se auditan.

**Migración `0011`** (`tutoring_followups` ya existía con `id, student_id, tutor_id, followup_date, note, created_at`): añade `group_id` (nullable solo por las filas previas; la aplicación siempre lo fija) con FK compuesta `(student_id, group_id) → enrollments`, `CHECK` de nota no vacía (cualquier espacio en blanco cuenta como vacío) y ≤ 4000 caracteres, trigger append-only e índice `(group_id, followup_date DESC, created_at DESC)`. Probada en BD vacía y como upgrade con una fila previa.

**Diferencias con el prompt/diccionario.** La tabla solo tiene **una** nota de texto: no hay campos separados de observación, acción o acuerdo, ni estado, ni fecha de próxima revisión. No se inventaron columnas para ellos. Por eso **no existen** los "seguimientos pendientes" (acuerdos abiertos, próximas revisiones) ni la actualización de estado: quedan como decisión abierta. La nota puede contener acciones/acuerdos como texto libre.

**Decisiones abiertas de Tutoring**
- Si el seguimiento necesita campos estructurados (acción, acuerdo, próxima revisión, estado) para soportar "pendientes"; sin ellos no se implementa esa consulta.
- Si un tutor nuevo debe ver los seguimientos de **otros grupos/periodos** previos del alumno (hoy solo los de los grupos que tutela ahora).
- Si el DIRECTOR (o el alumno, con acuerdos visibles) debe poder escribir o leer algo más; si se permiten correcciones (hoy no: se registra otro seguimiento).
- Si un periodo cerrado debe bloquear también los seguimientos (hoy sí, por coherencia con el resto).
- Si el seguimiento aplica a alumnos con inscripción `WITHDRAWN` (hoy no se pueden añadir; el historial se conserva).

### Analytics (`/api/analytics`)
Indicadores académicos verificables y alertas explicables para el Tutor y el Director. **Todo se deriva de las tablas de los otros módulos en cada lectura** (nada se duplica ni se guarda como snapshot); lo único que Analytics escribe es la tabla `alerts`. No hay predicción, puntaje de riesgo, clasificación de alumnos ni análisis del texto de los seguimientos.

Autorización (rol + relación vigente, releída en cada llamada): **TUTOR** solo grupos con `groups.tutor_id` = él ahora (si el grupo cambia de tutor, el anterior pierde el acceso al instante y el nuevo hereda alertas e historial); **DIRECTOR** todos los grupos (el modelo no relaciona director con carrera, no se inventó esa relación). ADMIN, TEACHER y STUDENT reciben 403 en todo: las alertas están reservadas a Tutor y Director y no hay vista del alumno ni del profesor en el MVP.

| Método | Ruta | Descripción |
|---|---|---|
| GET | `/students/{id}?group_id=` | indicadores de UN alumno en UN grupo/periodo (sin `group_id`: la inscripción más reciente a la que el llamante tiene acceso) |
| GET | `/groups/{id}` | indicadores agregados de los alumnos ACTIVOS del grupo |
| GET | `/overview?academic_term_id=&career_id=` | una línea agregada por grupo (SQL), TUTOR: sus grupos · DIRECTOR: todos |
| GET | `/alerts?student_id=&group_id=&academic_term_id=&type=&status=&limit=&offset=` | alertas **persistidas** dentro del alcance; **solo se actualizan bajo demanda** con `POST /alerts/recalculate` (un GET nunca recalcula y puede estar desactualizado: ver `evaluated_at`). `status`: vacío = sin resolver (OPEN+ACKNOWLEDGED), `OPEN`, `ACKNOWLEDGED`, `RESOLVED`, `ALL` |
| GET | `/alerts/{id}` | una alerta persistida (tan fresca como el último recálculo bajo demanda; ver `evaluated_at`) |
| POST | `/alerts/recalculate` `{"group_id":"…","student_id":"…"?}` | evalúa **bajo demanda** las reglas de UN grupo (o de un alumno suyo) y actualiza las alertas persistidas; tutor del grupo o DIRECTOR |

```http
GET /api/analytics/students/{id}?group_id=…
-> 200 {"group":{"id","code","level"},"career":{…},"academic_term":{"id","name","status","start_date","end_date"},
        "student":{"id","institutional_number","first_name","last_name"},"enrollment_status":"ACTIVE",
        "subjects":[{"subject_enrollment_id","teaching_assignment_id","subject":{"code","name"},"attempt_number","status",
          "grades":{"units_total":7,"units_graded":2,"units_pending":5,"na":1,"sa":0,"de":1,"au":0,
                    "na_units":[{"subject_unit_id","unit_number","unit_name","score":76.00,"category":"NA"}]},
          "attendance":{"closed_session_records":11,"present":9,"late":1,"absent":1,"justified":0,"unjustified_absences":1,
                        "attendance_pct":90.91,"lates_per_absence":3,"absences_from_lates":0,"effective_absences":1},
          "attendance_trend":{"window_sessions":5,"recent":{…,"attendance_pct":80.00},"previous":{…,"attendance_pct":100.00},"drop_points":20.00}|null,
          "justifications":{"requests_total","pending_tutor","pending_teacher","tutor_rejected","teacher_rejected","approved",
                            "absences_with_request","oldest_pending_request_date"}}],
        "totals":{"grades":{…},"attendance":{…},"justifications":{…}},
        "tutoring":{"followups_count":2,"last_followup_date":"2026-03-01"},
        "signals":[{"rule","rule_description","message","evidence":{…}}],     // calculadas EN VIVO
        "alerts":[<alerta persistida>…]}                                       // tal como quedaron en el último recálculo

GET /api/analytics/alerts -> 200 {"data":[{"id","rule","rule_description","status","message","evidence":{…},
        "student":{…},"group":{"id","code"},"academic_term":{"id","name"},"subject":{"subject_enrollment_id","code","name"}|null,
        "created_at","updated_at","evaluated_at","resolved_at"}],"total","limit","offset"}

POST /api/analytics/alerts/recalculate -> 200 {"group_id","student_id","evaluated_at","created","updated","resolved","unchanged","open_alerts":{"GRADE_NA_UNIT":1,…}}
```
`/groups/{id}` devuelve además `active_students`, `grades`, `attendance`, `justifications`, `tutoring{students_with_followup, students_without_followup, followups_count}`, `signals_now` (señales vivas por regla), `open_alerts` (persistidas por regla), `by_assignment[]` (una entrada por asignación docente; nunca se mezclan entre sí) y `students[]` (fila compacta por alumno).

**Métricas — fórmulas exactas**
- *Registros considerados* (`closed_session_records`) = registros de asistencia de sesiones **CERRADAS** (`attendance_sessions.status = 'CLOSED'`) de las inscripciones a materia. Las sesiones abiertas se excluyen: antes del cierre, la falta de un registro no significa ausencia.
- `present`, `late`, `absent`, `justified` = conteo por estado. `unjustified_absences` = `absent` (las faltas `JUSTIFIED` son otro estado; una solicitud pendiente **no** justifica nada).
- `attendance_pct` (**asistencia ajustada por justificaciones**) = **(present + late) / (present + late + absent)**. Numerador: asistencias puntuales y retardos. Denominador: todos los registros cerrados **excepto los `JUSTIFIED`**, que se excluyen (una falta justificada no cuenta como asistencia ni penaliza). Por eso NO es "asistencias / sesiones totales": con registros `JUSTIFIED` el porcentaje es mayor que esa razón simple, y se puede recuperar el cálculo sin ajuste con los conteos (`present`, `late`, `absent`, `justified`). Redondeo a centésimas con enteros. Sin denominador → `null` (nunca 0 %). Los porcentajes de grupo se calculan sumando numeradores y denominadores, no promediando porcentajes.
- `absences_from_lates` = ⌊late / `lates_per_absence`⌋ y `effective_absences` = `absent` + `absences_from_lates`: es **exactamente** la regla de Attendance (reutilizada, no reimplementada); indicador, los retardos nunca se reescriben como faltas, `JUSTIFIED` no suma. `lates_per_absence` sale de la sesión abierta más recientemente de la asignación (`null` sin sesiones: no se deriva nada). En totales, la equivalencia se aplica por materia y luego se suma (no se mezclan retardos entre materias).
- *Tendencia*: con `N` = `ANALYTICS_TREND_WINDOW`, compara el `attendance_pct` de los últimos `N` registros cerrados contra los `N` anteriores. Solo existe con al menos `2N` registros y con denominador en ambas ventanas; si no, `attendance_trend` es `null`. `drop_points` = anterior − reciente.
- *Calificaciones*: `units_total` = unidades de la materia; `units_graded` = unidades con calificación capturada; `units_pending` = el resto (**no son NA ni 0.00**). NA < 80.00 · SA 80.00–89.99 · DE 90.00–99.99 · AU = 100.00, con centésimos enteros (reglas de Grades). No hay promedios ni calificación final (sin política institucional). Cada intento (`attempt_number`) y cada periodo se calcula por separado.
- *Justificantes*: se cuentan **solicitudes** y **ausencias** por separado (`absences_with_request` = registros de asistencia distintos con al menos una solicitud: varias solicitudes rechazadas de la misma falta son varias solicitudes, una falta). `PENDING_*` no es una justificación definitiva; la fuente de verdad de una falta justificada es el estado `JUSTIFIED` en Attendance.
- *Tutoring*: solo `followups_count` y `last_followup_date`; nunca el texto. "Sin seguimiento" es un dato, no una valoración del tutor.

**Alertas** (tabla `alerts`, `type` = código de regla; los umbrales son **configuraciones propuestas para el MVP (valores por defecto elegidos por el equipo técnico), NO reglas oficiales de la universidad**, ajustables por entorno sin tocar otros módulos):

| Regla | Condición | Clave de condición | Umbral |
|---|---|---|---|
| `GRADE_NA_UNIT` | una unidad capturada con score < 80.00 | `unit:<inscripción>:<unidad>` | — |
| `UNJUSTIFIED_ABSENCES` | `absent` ≥ umbral en una materia (sesiones cerradas) | `absences:<inscripción>` | `ANALYTICS_ABSENCES_THRESHOLD` = 3 |
| `ATTENDANCE_DROP` | `attendance_pct` previo − reciente ≥ puntos, con ≥ 2N registros | `drop:<inscripción>` | `ANALYTICS_TREND_WINDOW` = 5, `ANALYTICS_TREND_DROP_POINTS` = 20 |
| `PENDING_JUSTIFICATIONS` | solicitudes `PENDING_TUTOR`/`PENDING_TEACHER` del alumno en el grupo | `pending:<grupo>` | — |

Cada alerta guarda `message` (en español, con los datos reales), `evidence` (JSON: grupo, periodo, materia, intento, cifras y umbral aplicado), la regla, el contexto (`group_id`, y `subject_enrollment_id` cuando aplica) y cuándo se evaluó. No afirman que el alumno reprobará ni diagnostican; la ausencia de seguimientos no genera ninguna alerta.

**Recálculo y vigencia.** Leer **nunca** recalcula (los GET no escriben). `POST /alerts/recalculate` evalúa UN grupo (opcionalmente un alumno) en una transacción: crea las condiciones nuevas (`OPEN`), conserva las vigentes (una `ACKNOWLEDGED` sigue así; si el mensaje o la evidencia cambiaron se actualizan en la misma fila) y pasa a `RESOLVED` (con `resolved_at`) las que dejaron de cumplirse, p. ej. al corregir una nota NA a SA, justificar una falta o resolver una solicitud. Si la condición vuelve, se abre una alerta **nueva** y la resuelta queda como historial. Es idempotente: la identidad de una alerta es `(alumno, regla, clave de condición)`, con un índice único parcial sobre las no resueltas y un candado consultivo por grupo que serializa evaluaciones concurrentes. Solo aplica a inscripciones ACTIVAS; un periodo cerrado se congela (409 `term_closed`). Se escribe **un** evento de auditoría por ejecución que cambió algo (`analytics.alerts_recalculated` con ids y conteos, nunca contenido); una ejecución sin cambios no audita ni crea filas (solo avanza `evaluated_at`). **Ventana de consistencia:** las alertas guardadas reflejan el último recálculo; no hay recálculo automático ni programador (nada dispara el recálculo al capturar una nota, cerrar una sesión o resolver una solicitud). Las `signals` de `/students/{id}` y `signals_now` de `/groups/{id}` se calculan en vivo con los mismos datos y reglas, útiles para detectar alertas desactualizadas. Un cambio de alumno de grupo o de tutor se refleja al recalcular el grupo afectado.

Errores: 400 `invalid_body` · 401 · 403 `forbidden` · 404 `student_not_found`, `group_not_found`, `enrollment_not_found`, `alert_not_found` · 409 `term_closed` · 422 `invalid_filter`, `invalid_paging` (`limit` 1–200), `group_id_required`, `invalid_group_id`, `invalid_student_id`.

**Migración `0012`** (la tabla `alerts` de 0004 no tenía contexto académico, forma de identificar una condición ni fechas): añade `group_id`, `condition_key`, `updated_at`, `evaluated_at`, `resolved_at`, `CHECK` de coherencia `RESOLVED ⇔ resolved_at` y de tipos de regla conocidos, el índice único parcial `alerts_one_open_per_condition_idx` y un índice `(group_id, status)`. Probada en BD vacía y como upgrade con una alerta previa.

**Limitaciones y decisiones abiertas de Analytics**
- Los umbrales (3 faltas, ventanas de 5, 20 puntos) son propuestas; falta que la institución defina umbrales oficiales, plazos de justificación y criterios de seguimiento mínimo.
- No hay `acknowledge` de alertas (el estado `ACKNOWLEDGED` existe en el esquema y se respeta, pero ningún endpoint lo asigna), ni recálculo automático, ni notificaciones.
- El DIRECTOR no está acotado por carrera (el modelo no lo relaciona). TEACHER y STUDENT no tienen vista de Analytics.
- Pendientes de Tutoring (acuerdos, próxima revisión, estado) no existen como datos, así que no hay métricas de ellos.
- Los GET de grupo cargan los hechos del grupo (6 consultas constantes, acotadas por grupo); `/overview` usa agregados SQL por grupo. Si un grupo creciera mucho habría que medir antes de añadir vistas materializadas.

Errores: `{"error":{"code","message","request_id"}}` — 400 cuerpo inválido, 422 validación, 401, 403, 404, 409, 429 (con `Retry-After`), 500.

## Estructura
```
cmd/{api,migrate,seed}   internal/{app,platform,identity,academic,grades,attendance,justifications,tutoring,analytics,audit}   migrations/
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
