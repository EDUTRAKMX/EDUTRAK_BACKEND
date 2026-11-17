[UNIDAD_1.md](https://github.com/user-attachments/files/34984214/UNIDAD_1.md)
# EDUTRAK — Unidad 1: análisis, planeación y diseño

## Ficha del proyecto

| Campo | Información |
|---|---|
| Institución | Universidad Tecnológica de Torreón |
| Programa | Ingeniería en Desarrollo y Gestión de Software |
| Asignatura | Desarrollo Web Profesional |
| Proyecto | **EDUTRAK — Plataforma Web de Seguimiento Académico y Acompañamiento Tutorial** |
| Docente | **MCC. Jesús Del Toro Robledo** |
| Grado y grupo | 8.º A |
| Actividad | Avances del proyecto — Unidad 1 |
| Fecha de elaboración | 08/10/2026 |
| Integrantes | Jaret Eduardo González Carrasco (22170143); Juan Pablo Juárez González (23170112); Daniela Cabello González (23170179); Ernesto Herrera Martínez (23170202) |

---

## 1. Problemática y justificación

### 1.1 Problemática

EDUTRAK surge de una observación del equipo dentro de su grupo académico: la falta de una fuente centralizada impide verificar con precisión quién falta, con qué frecuencia, en qué materias y si la inasistencia se relaciona con cambios en el rendimiento académico. En la UTT, calificaciones, asistencias, retardos, faltas y justificaciones pueden estar distribuidos entre registros docentes y procesos administrativos, lo que dificulta el seguimiento continuo.

Se identifican cuatro problemas principales:

1. **Incertidumbre sobre inasistencias:** falta de visibilidad histórica y consolidada por materia y periodo.
2. **Seguimiento tutorial fragmentado:** el Tutor requiere consultar asistencia y calificaciones juntas y trabajar con evidencia.
3. **Pase de lista ineficiente o vulnerable:** consume tiempo y, si se digitaliza sin controles, permite suplantación o intentos fuera del campus.
4. **Justificaciones desconectadas:** se necesita un flujo trazable con motivo, evidencia, revisión del Tutor y decisión final del Profesor.

### 1.2 Justificación

- **Teórica:** centralización, trazabilidad, control de acceso e indicadores basados en datos, sin sustituir las decisiones humanas.
- **Práctica:** reducir el tiempo del pase de lista, consultar historial, capturar calificaciones, digitalizar justificantes y ofrecer al Tutor información objetiva. El QR dinámico, NFC adicional y la validación de red ayudan a reducir registros fraudulentos.
- **Metodológica:** separación por módulos de identidad, operación académica, asistencia, calificaciones, justificaciones, tutoría, analítica y auditoría para desarrollar y probar de forma incremental.

## 2. Objetivos

### 2.1 Objetivo general

Desarrollar una plataforma web de seguimiento académico y acompañamiento tutorial que centralice las calificaciones y el control de asistencias, gestione el pase de lista mediante QR dinámico, NFC y alternativa manual con validación de red, permita al Alumno consultar su información y solicitar justificantes con evidencia, y genere indicadores explicables para apoyar el seguimiento del Tutor.

### 2.2 Objetivos específicos

1. Diseñar e implementar asistencia por QR dinámico, NFC y registro manual con validación de red institucional y retardo configurable.
2. Desarrollar el portal del Alumno para consultar materias, calificaciones, asistencia, faltas, retardos y solicitudes.
3. Implementar justificación de faltas con motivo, descripción, evidencia, revisión del Tutor y decisión final del Profesor.
4. Construir el panel del Tutor con indicadores, alertas explicables e historial de seguimiento.
5. Incorporar roles y trazabilidad de operaciones sensibles utilizando cuentas institucionales precargadas.
6. Organizar el backend como monolito modular en Go, con Arquitectura Hexagonal por módulo y PostgreSQL.

## 3. Alcances, limitaciones y MVP

### 3.1 Roles funcionales del sistema

| Rol | Responsabilidad principal | Funciones previstas |
|---|---|---|
| **Administrador del sistema** | Cuentas y soporte de acceso | Restablecer contraseñas de alumnos y profesores; cada usuario también puede cambiar la propia. |
| **Director de Carrera** | Configuración y supervisión académica | Crear/activar/cerrar periodos, administrar grupos, asignar tutores, materias, profesores, horarios y consultar indicadores. |
| **Profesor** | Operar materias y grupos asignados | Consultar grupos, capturar calificaciones, gestionar sesiones de asistencia con QR/NFC/manual y decidir justificantes remitidos. |
| **Tutor** | Seguimiento a alumnos asignados | Consultar asistencia/calificaciones, revisar justificantes, registrar observaciones, acuerdos y acciones, interpretar alertas. |
| **Alumno** | Consultar su situación y realizar solicitudes | Ver materias, horario, calificaciones y asistencia; registrar QR desde su cuenta; solicitar justificantes y consultar estados. |

### 3.2 Alcance funcional

- QR dinámico como mecanismo principal de asistencia del MVP; NFC como mecanismo adicional; registro manual disponible al Profesor.
- Validación del registro QR con la red institucional autorizada.
- Operación académica de periodos, grupos, materias, alumnos precargados, tutores, profesores y horarios.
- Calificaciones por unidad en escala **0.00–100.00**, conservando decimales.
- Categorías **NA**, **SA**, **DE** y **AU**, calculadas con el valor exacto, sin redondearlo.
- Portal del Alumno, justificantes con evidencia, seguimiento tutorial, indicadores básicos, roles y auditoría de operaciones sensibles.

### 3.3 MVP priorizado

| Prioridad | Componente | Definición en la Unidad 1 |
|---|---|---|
| Alta | Autenticación y roles | Cuentas institucionales precargadas, cambio y restablecimiento de contraseña. |
| Alta | Operación académica básica | Periodos, grupos, listas, materias, profesores, tutor y horarios. |
| Alta | Asistencia QR + manual | Apertura de sesión por Profesor, QR para Alumno autenticado, validación de red y captura manual. |
| Alta | Portal del Alumno | Materias, calificaciones, asistencia y solicitudes. |
| Alta | Calificaciones | Captura por unidad y clasificación NA/SA/DE/AU. |
| Alta | Justificantes | Solicitud Alumno → revisión Tutor → decisión Profesor. |
| Alta | Dashboard básico del Tutor | Indicadores objetivos de asistencia y rendimiento. |
| Media | NFC | Captura adicional posterior al flujo QR estable. |
| Media | Analítica avanzada y reportes | Indicadores sofisticados y reportes agregados. |
| Media | Auditoría ampliada | Mayor cobertura de eventos y consultas administrativas. |

### 3.4 Reglas de negocio definidas en el proyecto

1. Matrícula institucional de **8 dígitos** (ejemplo ficticio de formato: `23170202`).
2. Correo institucional con patrón **`matrícula@uttcampus.edu.mx`** (formato establecido en el diseño del sistema).
3. Grupos y listas de alumnos precargados antes de la operación docente.
4. El usuario cambia su contraseña; el Administrador puede restablecerla.
5. Calificaciones de **0.00 a 100.00** con decimales, sin convertirlas a escala de 10 ni redondearlas para clasificar.
6. **NA:** menor que 80.00; **SA:** 80.00 a menor que 90.00; **DE:** 90.00 a menor que 100.00; **AU:** exactamente 100.00.
7. Retardo estándar de **15 minutos**, configurable por el Profesor.
8. Equivalencia estándar de **3 retardos = 1 falta**, configurable por el Profesor.
9. El Profesor genera/muestra el QR y el Alumno lo escanea desde su sesión autenticada.
10. Evidencias aceptadas: **PDF, JPG, JPEG y PNG**, máximo **10 MB por archivo**.

### 3.5 Limitaciones

- **NFC:** requiere un dispositivo compatible del Profesor; se mantiene QR o captura manual como alternativa.
- **Conectividad:** interrupciones de red pueden afectar la validación institucional.
- **Alcance de las alertas:** apoyan decisiones humanas y no emiten diagnósticos ni veredictos automáticos de reprobación.
- **Calendario:** las fechas de las etapas posteriores se consideran provisionales.
- **Prototipos:** faltan vistas complementarias (escaneo QR del Alumno, gestión de contraseñas por Administrador y versiones móviles de Tutor y Director), así como validación formal de identidad institucional.

## 4. Requisitos funcionales y no funcionales

La prioridad **Alta** corresponde al MVP y la **Media** a componentes complementarios o posteriores. Se presentan los requisitos con sus identificadores y criterios de aceptación.

### 4.1 Requisitos funcionales (RF-01 a RF-25)

| ID | Prioridad | Requisito | Criterio de aceptación |
|---|---|---|---|
| **RF-01** | Alta | Inicio de sesión con cuenta institucional precargada. | Credenciales válidas permiten acceder solo a las funciones del rol. |
| **RF-02** | Alta | Cambiar la contraseña propia. | Exige contraseña actual y confirmación de la nueva; la nueva queda vigente. |
| **RF-03** | Alta | Restablecimiento de contraseña por el Administrador para alumnos y profesores. | Restablece la credencial sin modificar información académica. |
| **RF-04** | Alta | Crear, activar y cerrar periodos académicos por el Director. | El estado del periodo determina si admite operación académica. |
| **RF-05** | Alta | Administrar grupos con listas de alumnos precargadas. | La lista del grupo está disponible antes de asignar clases. |
| **RF-06** | Alta | Asignar tutor, materias, profesores y horarios a grupos. | Asignaciones consultables por Director y usuarios involucrados. |
| **RF-07** | Alta | Consulta docente de grupos, materias, horario y alumnos asignados. | El Profesor solo ve asignaciones vigentes relacionadas con su cuenta. |
| **RF-08** | Alta | Capturar calificaciones por unidad de 0.00 a 100.00 con decimales. | Rechaza valores fuera del rango y conserva decimales. |
| **RF-09** | Alta | Clasificar la calificación NA/SA/DE/AU. | NA < 80; SA 80–<90; DE 90–<100; AU = 100, sin redondeo. |
| **RF-10** | Alta | Abrir/cerrar una sesión de asistencia por el Profesor. | Vincular materia/grupo, fecha, estado y Profesor responsable. |
| **RF-11** | Alta | Generar QR dinámico para la sesión. | QR identifica sesión vigente y se puede invalidar/renovar. |
| **RF-12** | Alta | Registrar asistencia por QR desde la cuenta del Alumno. | Solo Alumno autenticado e inscrito registra su propia asistencia. |
| **RF-13** | Alta | Validar QR desde red institucional autorizada. | Registro no se confirma si falla la red; existe alternativa manual. |
| **RF-14** | **Media** | Capturar asistencia por NFC con dispositivo del Profesor. | NFC, si está disponible, crea registro equivalente al QR/manual. |
| **RF-15** | Alta | Capturar/corregir asistencia manualmente. | El Profesor puede registrar Presente, Retardo, Falta o corrección en ventana permitida. |
| **RF-16** | Alta | Configurar umbral de retardo por clase. | Estándar 15 minutos, modificable por el Profesor. |
| **RF-17** | Alta | Configurar cantidad de retardos equivalentes a falta. | Estándar de tres, modificable según la clase. |
| **RF-18** | Alta | Consulta por Alumno de asistencia, faltas, retardos y notas. | Solo información personal del periodo correspondiente. |
| **RF-19** | Alta | Solicitar justificación sobre una falta existente. | Solicitud solo a partir de una falta propia. |
| **RF-20** | Alta | Adjuntar evidencia al justificante. | PDF/JPG/JPEG/PNG, máximo 10 MB por archivo. |
| **RF-21** | Alta | Revisión/aprobación/rechazo del Tutor. | Registra responsable, fecha, comentario y cambia estado. |
| **RF-22** | Alta | Decisión final del Profesor sobre justificación remitida. | Aceptación justifica la falta; rechazo la conserva. |
| **RF-23** | Alta | Seguimiento tutorial con observaciones, acuerdos y acciones. | Conserva alumno, tutor, periodo, fecha y contenido. |
| **RF-24** | Alta | Indicadores y alertas básicos de asistencia/rendimiento. | Muestran datos que originaron la alerta, sin diagnósticos. |
| **RF-25** | **Media** | Auditoría de operaciones sensibles. | Registra actor, acción, entidad, fecha y metadatos. |

### 4.2 Requisitos no funcionales (RNF-01 a RNF-12)

| ID | Tipo | Requisito | Criterio de verificación |
|---|---|---|---|
| **RNF-01** | Seguridad | Autorización en backend, no solo botones ocultos de Angular. | Pruebas por rol bloquean recursos ajenos. |
| **RNF-02** | Identidad | Matrícula y correo institucional únicos y coherentes. | Matrícula de 8 dígitos, correo con patrón `matrícula@uttcampus.edu.mx`, sin duplicados. |
| **RNF-03** | Integridad | Proteger relaciones entre grupos, alumnos, materias, notas, asistencias y justificantes. | BD rechaza referencias inválidas e infracciones de restricciones. |
| **RNF-04** | Privacidad | Evidencias y datos académicos solo para usuarios autorizados. | Pruebas impiden acceso a expedientes ajenos. |
| **RNF-05** | Usabilidad | Interfaz consistente y adaptable a escritorio/móvil. | Tareas principales completables en ambas resoluciones. |
| **RNF-06** | Disponibilidad | Asistencia manual si QR/NFC o red fallan. | Profesor puede continuar pase de lista y registrar/corregir después. |
| **RNF-07** | Rendimiento | Registros consecutivos de asistencia sin bloqueo. | Prueba de carga sin pérdidas ni duplicados. |
| **RNF-08** | Mantenibilidad | Monolito modular y Arquitectura Hexagonal. | Módulos comprobables sustituyendo adaptadores externos. |
| **RNF-09** | Trazabilidad | Registrar actor, fecha y cambio en decisiones sensibles. | Auditoría reconstruye la operación. |
| **RNF-10** | Archivos | Validar extensión y tamaño en frontend y backend. | Rechazo claro de archivos no permitidos o mayores de 10 MB. |
| **RNF-11** | Compatibilidad | Navegadores modernos. | MVP probado en Chrome/Edge escritorio y navegador móvil actual. |
| **RNF-12** | Calidad de datos | Conservar decimal exacto y categoría sin redondear. | Probar 79.99, 80.00, 89.99, 90.00, 99.99 y 100.00. |

## 5. Análisis de riesgos

Se emplea una **matriz 5×5** de probabilidad (P) e impacto (I), ambos de 1 a 5. **Nivel = P × I**.

| Nivel numérico | Clasificación |
|---|---|
| 1–4 | Bajo |
| 5–9 | Medio |
| 10–16 | Alto |
| 17–25 | Crítico |

### 5.1 Riesgo inherente (R1 a R15)

| ID | Categoría | Riesgo | P | I | Nivel inherente |
|---|---|---|---:|---:|---|
| **R1** | Gestión | Cambios de requisitos durante el desarrollo. | 3 | 3 | **9 Medio** |
| **R2** | Técnico | Errores o cambios tardíos del modelo BD. | 2 | 4 | **8 Medio** |
| **R3** | Técnico | Incompatibilidad/falla NFC o QR. | 3 | 4 | **12 Alto** |
| **R4** | Equipo | Indisponibilidad de integrante clave. | 2 | 4 | **8 Medio** |
| **R5** | Externo/operación | Falla de red institucional durante asistencia. | 3 | 4 | **12 Alto** |
| **R6** | Seguridad/operación | IP/subred rechaza legítimos o permite no autorizados. | 2 | 4 | **8 Medio** |
| **R7** | Seguridad | Suplantación mediante capturas, reenvío, VPN u otro método. | 3 | 5 | **15 Alto** |
| **R8** | Seguridad | Permisos incorrectos permiten acceso a datos ajenos. | 2 | 5 | **10 Alto** |
| **R9** | Datos | Pérdida, duplicación o inconsistencia de registros. | 2 | 5 | **10 Alto** |
| **R10** | Funcional | Fallos al cargar evidencias impiden justificar. | 2 | 3 | **6 Medio** |
| **R11** | Privacidad | Exposición de datos académicos/documentos. | 2 | 5 | **10 Alto** |
| **R12** | Analítica | Cálculo incorrecto de alertas. | 2 | 4 | **8 Medio** |
| **R13** | Rendimiento | Lentitud con asistencias consecutivas. | 2 | 3 | **6 Medio** |
| **R14** | Integración | Fallas al integrar Angular, Go, QR/NFC y BD. | 3 | 4 | **12 Alto** |
| **R15** | Tiempo | Retrasos reducen tiempo para pruebas y correcciones. | 3 | 4 | **12 Alto** |

### 5.2 Prevención, contingencia, responsables y riesgo residual

| ID | Prevención | Contingencia | Responsable(s) | Riesgo residual |
|---|---|---|---|---|
| **R1** | Validar requisitos y controlar cambios en Jira. | Replanificar sprint/alcance y priorizar MVP. | Jaret | **4 Bajo** (2×2) |
| **R2** | Revisar modelo, usar migraciones y pruebas. | Recuperar respaldo o migración previa y corregir relaciones. | Ernesto / Jaret | **3 Bajo** (1×3) |
| **R3** | Probar dispositivos y QR temprano. | Usar QR o registro manual si falla NFC. | Juan Pablo | **6 Medio** (2×3) |
| **R4** | Documentar, compartir avances y tener respaldos de rol. | Reasignar al integrante de respaldo. | Jaret | **3 Bajo** (1×3) |
| **R5** | Verificar conectividad y preparar alternativa manual. | Registro manual temporal y sincronizar/corregir. | Juan Pablo | **6 Medio** (2×3) |
| **R6** | Probar subredes reales y reglas configurables. | Captura manual controlada y revisión de red. | Jaret / Juan Pablo | **3 Bajo** (1×3) |
| **R7** | QR corto, red validada y detección de duplicados. | Invalidar registros sospechosos, auditar y corregir. | Jaret / Juan Pablo | **8 Medio** (2×4) |
| **R8** | RBAC en servidor y pruebas por casos de uso. | Revocar permisos/sesiones, corregir y auditar. | Jaret | **4 Bajo** (1×4) |
| **R9** | Restricciones, transacciones, IDs únicos y respaldos. | Recuperar respaldo o corregir datos controladamente. | Ernesto | **4 Bajo** (1×4) |
| **R10** | Validar archivo y mostrar mensajes claros. | Permitir reintento sin perder formulario y escalar soporte. | Daniela / Juan Pablo | **2 Bajo** (1×2) |
| **R11** | Control de acceso, sesiones y archivos privados. | Revocar accesos, investigar y retirar recursos expuestos. | Jaret | **4 Bajo** (1×4) |
| **R12** | Reglas explicables y pruebas con casos conocidos. | Suspender alerta incorrecta, recalcular y corregir. | Ernesto | **3 Bajo** (1×3) |
| **R13** | Pruebas de carga y optimización de consultas/índices. | Desactivar procesos no críticos y optimizar. | Ernesto / Jaret | **2 Bajo** (1×2) |
| **R14** | Integración incremental y contratos API claros. | Aislar módulo, volver a versión funcional y corregir. | Jaret / Juan Pablo | **6 Medio** (2×3) |
| **R15** | Jira, hitos cortos y margen de correcciones. | Reducir a MVP, redistribuir tareas y priorizar defectos críticos. | Jaret | **6 Medio** (2×3) |

**Riesgos prioritarios:** **R7** (fraude de asistencia), **R14** (integración) y **R15** (plazos). El riesgo residual representa la estimación tras aplicar las medidas propuestas.

## 6. Elección de metodología y roles del equipo

### 6.1 Metodología

**Scrum ligero** apoyado por **Jira**. El equipo utiliza backlog, sprints cortos, revisiones y retrospectivas. El tablero puede adoptar un flujo Kanban: **Pendiente → En proceso → En revisión → Terminado**, sin reemplazar los ciclos Scrum. Las reglas y fechas se ajustarán conforme se confirmen requisitos y calendario docente.

### 6.2 Roles de desarrollo

| Integrante | Rol asignado en Unidad 1 | Responsabilidad |
|---|---|---|
| Jaret Eduardo González Carrasco | Project Lead / Software Architect / Backend & Security Engineer | Alcance, arquitectura, backend y seguridad. |
| Juan Pablo Juárez González | Full-Stack Engineer / Integración de asistencia | Módulo de asistencia e integración NFC/QR; automatizaciones. |
| Daniela Cabello González | Frontend & UX Engineer / Análisis de procesos | Procesos escolares, interfaz y wireframes. |
| Ernesto Herrera Martínez | Data/Analytics & QA Engineer | Riesgos, RNF, datos, indicadores y pruebas. |

### 6.3 Plan de contingencia de roles

| Responsable principal | Respaldo | Cobertura |
|---|---|---|
| Jaret | Juan Pablo | Backend e integración; Ernesto puede apoyar datos/QA. |
| Juan Pablo | Jaret | Servicios de asistencia; Daniela apoya integración visual. |
| Daniela | Juan Pablo | Pantallas e integración; Jaret apoya contrato/API. |
| Ernesto | Jaret | Datos y QA; Juan Pablo ayuda en ejecución de pruebas. |

## 7. Separación del proyecto en tareas

El plan comprende **26 tareas**: 8 de la Unidad 1, 10 de la Unidad 2 y 8 de la Unidad 3. Se incluyen responsables, estimaciones de esfuerzo, dependencias y fechas planeadas.

| ID | Tarea | Responsable(s) | Esfuerzo | Depende de | Fecha planeada | Provisional |
|---|---|---|---:|---|---|---|
| U1-01 | Planteamiento, alcance y MVP | Jaret + equipo | 4 h | — | 06–07 Oct | No |
| U1-02 | RF/RNF y aceptación | Ernesto + Jaret | 6 h | U1-01 | 06–07 Oct | No |
| U1-03 | Scrum, Jira, roles y contingencias | Jaret + Ernesto | 3 h | — | 06–07 Oct | No |
| U1-04 | Arquitectura, BD y reglas | Jaret + Ernesto | 5 h | U1-01 | 06–07 Oct | No |
| U1-05 | Wireframes iniciales | Daniela | 8 h | U1-01 | 07–12 Oct | Pendiente |
| U1-06 | Entrega del documento de avance | Todo el equipo | 2 h | U1-01…04 | 08 Oct | No |
| U1-07 | Preparación exposición/venta | Todo el equipo | 8 h | U1-06 | 09–12 Oct | No |
| U1-08 | Exposición/venta | Todo el equipo | 2 h | U1-07 | 13 Oct | No |
| U2-01 | Configurar repositorio, Jira y entorno | Jaret | 6 h | U1-08 | 14–16 Oct | Sí |
| U2-02 | Migraciones y esquema PostgreSQL | Ernesto + Jaret | 12 h | U1-08 | 14–20 Oct | Sí |
| U2-03 | Identidad, autenticación y roles | Jaret | 16 h | U2-01, U2-02 | 19–23 Oct | Sí |
| U2-04 | Operación académica base | Jaret + Juan Pablo | 20 h | U2-02 | 19–28 Oct | Sí |
| U2-05 | Shell Angular y navegación por rol | Daniela | 18 h | U2-01 | 19–28 Oct | Sí |
| U2-06 | Sesiones de asistencia QR/manual | Juan Pablo | 24 h | U2-03, U2-04 | 26 Oct–06 Nov | Sí |
| U2-07 | Escaneo QR y validación de red | Juan Pablo + Daniela | 18 h | U2-05, U2-06 | 29 Oct–06 Nov | Sí |
| U2-08 | Calificaciones por unidad | Ernesto + Jaret | 16 h | U2-03, U2-04 | 29 Oct–06 Nov | Sí |
| U2-09 | Pruebas de integración U2 | Ernesto + equipo | 12 h | U2-06…08 | 09–12 Nov | Sí |
| U2-10 | Demo/revisión provisional U2 | Todo el equipo | 3 h | U2-09 | 13 Nov | Sí |
| U3-01 | Flujo de justificantes | Jaret + Juan Pablo | 20 h | U2-10 | 17–25 Nov | Sí |
| U3-02 | Evidencias y vistas justificantes | Daniela + Juan Pablo | 18 h | U3-01 | 19–27 Nov | Sí |
| U3-03 | Dashboard Tutor e indicadores | Ernesto + Daniela | 20 h | U2-08, U3-01 | 24 Nov–04 Dic | Sí |
| U3-04 | Prototipo NFC | Juan Pablo | 14 h | U2-06 | 24 Nov–04 Dic | Sí |
| U3-05 | Auditoría y seguridad | Jaret | 14 h | U3-01, U3-02 | 01–07 Dic | Sí |
| U3-06 | Pruebas finales funcionales/carga/integración | Ernesto + equipo | 18 h | U3-02…05 | 01–07 Dic | Sí |
| U3-07 | Correcciones y documentación | Todo el equipo | 12 h | U3-05, U3-06 | 07–08 Dic | Sí |
| U3-08 | Entrega/demo final provisional | Todo el equipo | 3 h | U3-07 | 08 Dic | Sí |

**Criterios de finalización:** U1-01 alcance/MVP validado; U1-02 RF/RNF revisados; U1-03 responsabilidades documentadas; U1-04 arquitectura/modelo consistentes; U1-05 wireframes anotados; U1-06 documento entregado; U1-07 pitch/material de apoyo; U1-08 presentación realizada; U2-01 entorno funcional; U2-02 BD inicial desplegable; U2-03 login/RBAC; U2-04 periodos/grupos/asignaciones; U2-05 navegación base; U2-06 asistencia funcional; U2-07 QR de extremo a extremo; U2-08 notas; U2-09 pruebas y correcciones; U2-10 incremento demostrable; U3-01 workflow Tutor→Profesor; U3-02 flujo Angular; U3-03 dashboard/alertas; U3-04 prueba NFC; U3-05 controles revisados; U3-06 evidencia QA; U3-07 versión candidata; U3-08 cierre provisional.

## 8. Cronograma

El cronograma establece los siguientes hitos. Las fechas posteriores a la Unidad 1 se consideran provisionales.

| Fecha | Hito | Estado de planificación |
|---|---|---|
| 08/10/2026 | Entrega documento de avance | Confirmada |
| 13/10/2026 | Exposición / venta del proyecto | Confirmada |
| 13/11/2026 | Revisión / demo Unidad 2 | Provisional |
| 08/12/2026 | Entrega / demo final | Provisional |
| 09–15/12/2026 | Margen de contingencia / correcciones | Provisional |

![Figura 1. Cronograma Gantt provisional de EDUTRAK](assets/unidad1/fig-01.jpg)

*Figura 1. Cronograma Gantt de EDUTRAK.*

## 9. Elección de estructura web y tecnologías

| Tecnología o decisión | Uso | Motivo de elección |
|---|---|---|
| **Angular** | Frontend | SPA por componentes y vistas por rol, escritorio/móvil. |
| **Go** | Backend | Servicios HTTP, concurrencia y organización modular. |
| **PostgreSQL** | Base de datos | Relaciones académicas, UUID, JSON y restricciones. |
| **Monolito modular** | Estructura backend | Un solo despliegue con límites claros entre módulos. |
| **Arquitectura Hexagonal** | Estructura interna | Separar dominio y casos de uso de HTTP/BD. |
| **Jira** | Gestión | Backlog, asignación de responsables y seguimiento. |

El proyecto se distribuye en **dos repositorios** (no es un único monorepositorio):

- [Backend Go — EDUTRAK_BACKEND](https://github.com/EDUTRAKMX/EDUTRAK_BACKEND)
- [Frontend Angular — EDUTRAK_FRONTEND](https://github.com/EDUTRAKMX/EDUTRAK_FRONTEND)

La rama principal del repositorio backend es `master`. La estructura modular propuesta se presenta a continuación.

### 9.1 Arquitectura Hexagonal de referencia

Cada módulo propone cuatro zonas:

1. **Domain:** entidades y reglas de negocio.
2. **Application:** casos de uso y coordinación de reglas.
3. **Ports:** interfaces que necesita la aplicación.
4. **Adapters:** implementaciones de HTTP, PostgreSQL e integraciones.

![Figura 2. Arquitectura Hexagonal de EDUTRAK](assets/unidad1/fig-02.jpg)

*Figura 2. Referencia conceptual de Arquitectura Hexagonal (Ports & Adapters).*

### 9.2 Estructura propuesta del backend en Unidad 1

```text
edutrak-backend/
├── cmd/api/main.go
├── internal/
│   ├── identity/{domain,application,ports,adapters}
│   ├── academic/{domain,application,ports,adapters}
│   ├── attendance/{domain,application,ports,adapters}
│   ├── grades/{domain,application,ports,adapters}
│   ├── justifications/{domain,application,ports,adapters}
│   ├── tutoring/{domain,application,ports,adapters}
│   ├── analytics/{domain,application,ports,adapters}
│   └── audit/{domain,application,ports,adapters}
├── migrations/
├── config/
├── go.mod
├── go.sum
└── README.md
```

El árbol representa la organización conceptual de módulos; la implementación incorpora componentes transversales de infraestructura y plataforma.

### 9.3 Esquema propuesto de base de datos

El esquema relacional agrupa identidad/control de acceso, catálogo y operación académica, calificaciones, asistencia, justificaciones, tutoría, analítica y auditoría. El diseño contempla **23 tablas**.

![Figura 3. Esquema relacional de EDUTRAK](assets/unidad1/fig-03.jpg)

*Figura 3. Diagrama relacional propuesto de EDUTRAK.*

## 10. Selección de patrones de diseño

| Patrón propuesto | Aplicación en el diseño | Justificación |
|---|---|---|
| **Strategy** | Alternativas de asistencia QR, NFC y manual. | Compartir reglas de autorización, sesión y duplicados pese a cambiar el método. |
| **Repository** | Usuarios, grupos, calificaciones, asistencia y justificaciones. | Separar casos de uso de persistencia PostgreSQL mediante puertos/adaptadores. |
| **State** | Flujo de solicitudes de justificación. | Restringir transiciones por estado y rol Tutor/Profesor. |
| **Observer / eventos de dominio** | Alertas, calificaciones y notificaciones. | Disparar reacciones sin acoplar cada consumidor a la operación original. |

Estos patrones sustentan las decisiones de diseño y la separación de responsabilidades entre componentes.

## 11. Wireframes

El diseño de interfaces comprende **45 pantallas de escritorio**, **8 pantallas móviles** y **un sistema base de diseño** realizados en Figma. Las figuras 4–23 presentan vistas representativas de estos prototipos.

### 11.1 Inventario y cobertura

| Rol / plataforma | Pantallas o flujos cubiertos en la propuesta |
|---|---|
| Director · escritorio | Inicio, periodos, grupos, tutor, asignaciones, horarios y auditoría. |
| Profesor · escritorio | Grupos, asistencia NFC/QR/manual, cierre, calificaciones, justificantes. |
| Alumno · escritorio | Inicio, horario, calificaciones, asistencia, justificantes, avisos. |
| Tutor · escritorio | Inicio, alumnos, seguimiento, alertas, analítica y revisión de justificantes. |
| Alumno y Profesor · móvil | Ocho pantallas de inicio/horario/notas/justificantes y NFC/QR/manual. |
| Compartidas · escritorio | Login, perfil, 403, 404, error general y estados reutilizables. |

### 11.2 Navegación y flujos principales

- **General:** acceso → identificación de rol → tablero y menú autorizado; cambio personal de contraseña y restablecimiento con Administrador.
- **Director:** periodos → grupos → alumnos y tutores → asignaciones docentes y horarios → indicadores y auditoría según permiso.
- **Profesor:** grupos/materias → abrir sesión → elegir NFC/QR/manual → revisar asistencia → cerrar; capturar notas y decidir justificantes remitidos.
- **Alumno:** horario/materias → asistencia/notas → falta → justificar con motivo, descripción y evidencia → consultar estado.
- **Tutor:** alumnos asignados → alertas/historial → seguimiento → aprobar remisión al Profesor o rechazar con comentario.

### 11.3 Evidencia visual de pantallas de escritorio

Las figuras 4–19 muestran los prototipos de escritorio.

| Figura | Pantalla de la evidencia |
|---|---|
| 4 | Inicio de sesión institucional (pantalla 32 de 45). |
| 5 | Inicio del Director de Carrera (pantalla 1 de 45). |
| 6 | Lista de grupos (pantalla 4 de 45). |
| 7 | Asignaciones académicas (pantalla 6 de 45). |
| 8 | Crear sesión de asistencia (pantalla 11 de 45). |
| 9 | Asistencia NFC y cierre (pantalla 12 de 45). |
| 10 | Calificaciones por unidad (pantalla 14 de 45). |
| 11 | Inicio del Alumno (pantalla 16 de 45). |
| 12 | Historial de asistencia (pantalla 19 de 45). |
| 13 | Solicitud de justificante (pantalla 20 de 45). |
| 14 | Estado de justificante (pantalla 21 de 45). |
| 15 | Inicio del Tutor (pantalla 24 de 45). |
| 16 | Seguimiento tutorial (pantalla 27 de 45). |
| 17 | Alertas (pantalla 28 de 45). |
| 18 | Revisión del Tutor (pantalla 43 de 45). |
| 19 | Decisión final del Profesor (pantalla 45 de 45). |

![Figura 4. Inicio de sesión institucional](assets/unidad1/fig-04.jpg)

*Figura 4. Inicio de sesión institucional; prototipo de escritorio de la Unidad 1.*

![Figura 5. Inicio Director de Carrera](assets/unidad1/fig-05.jpg)

*Figura 5. Inicio Director de Carrera; prototipo de escritorio de la Unidad 1.*

![Figura 6. Listado de grupos](assets/unidad1/fig-06.jpg)

*Figura 6. Listado de grupos; prototipo de escritorio de la Unidad 1.*

![Figura 7. Asignaciones docentes](assets/unidad1/fig-07.jpg)

*Figura 7. Asignaciones docentes; prototipo de escritorio de la Unidad 1.*

![Figura 8. Inicio de asistencia](assets/unidad1/fig-08.jpg)

*Figura 8. Inicio de asistencia; prototipo de escritorio de la Unidad 1.*

![Figura 9. Asistencia mediante NFC](assets/unidad1/fig-09.jpg)

*Figura 9. Asistencia mediante NFC; prototipo de escritorio de la Unidad 1.*

![Figura 10. Captura de calificaciones](assets/unidad1/fig-10.jpg)

*Figura 10. Captura de calificaciones; prototipo de escritorio de la Unidad 1.*

![Figura 11. Portal del Alumno](assets/unidad1/fig-11.jpg)

*Figura 11. Portal del Alumno; prototipo de escritorio de la Unidad 1.*

![Figura 12. Historial de asistencia](assets/unidad1/fig-12.jpg)

*Figura 12. Historial de asistencia; prototipo de escritorio de la Unidad 1.*

![Figura 13. Solicitud de justificante](assets/unidad1/fig-13.jpg)

*Figura 13. Solicitud de justificante; prototipo de escritorio de la Unidad 1.*

![Figura 14. Estado del justificante](assets/unidad1/fig-14.jpg)

*Figura 14. Estado del justificante; prototipo de escritorio de la Unidad 1.*

![Figura 15. Dashboard del Tutor](assets/unidad1/fig-15.jpg)

*Figura 15. Dashboard del Tutor; prototipo de escritorio de la Unidad 1.*

![Figura 16. Seguimiento tutorial](assets/unidad1/fig-16.jpg)

*Figura 16. Seguimiento tutorial; prototipo de escritorio de la Unidad 1.*

![Figura 17. Listado de alertas](assets/unidad1/fig-17.jpg)

*Figura 17. Listado de alertas; prototipo de escritorio de la Unidad 1.*

![Figura 18. Revisión del Tutor](assets/unidad1/fig-18.jpg)

*Figura 18. Revisión del Tutor; prototipo de escritorio de la Unidad 1.*

![Figura 19. Decisión del Profesor](assets/unidad1/fig-19.jpg)

*Figura 19. Decisión del Profesor; prototipo de escritorio de la Unidad 1.*

### 11.4 Evidencia visual de pantallas móviles

Las 8 pantallas móviles se muestran en cuatro pares en las figuras 20–23:

| Figura | Diseños móviles |
|---|---|
| 20 | Alumno: inicio y horario diario (1–2 de 8). |
| 21 | Profesor: asistencia NFC y QR dinámico (3–4 de 8). |
| 22 | Profesor: captura manual; Alumno: notas (5–6 de 8). |
| 23 | Alumno: solicitud y estado de justificante (7–8 de 8). |

![Figura 20. Alumno: inicio y horario](assets/unidad1/fig-20.jpg)

*Figura 20. Alumno: inicio y horario; prototipos móviles de EDUTRAK.*

![Figura 21. Profesor: NFC y QR](assets/unidad1/fig-21.jpg)

*Figura 21. Profesor: NFC y QR; prototipos móviles de EDUTRAK.*

![Figura 22. Captura manual y calificaciones](assets/unidad1/fig-22.jpg)

*Figura 22. Captura manual y calificaciones; prototipos móviles de EDUTRAK.*

![Figura 23. Justificantes móviles](assets/unidad1/fig-23.jpg)

*Figura 23. Justificantes móviles; prototipos móviles de EDUTRAK.*

## 12. Búsquedas en el sitio web

| Rol | Búsqueda simple | Filtros avanzados propuestos |
|---|---|---|
| **Director de Carrera** | Alumnos, profesores, grupos, materias, periodos. | Carrera, cuatrimestre, periodo, grupo, tutor, estado. |
| **Profesor** | Alumnos de sus grupos, asistencia, notas y justificantes. | Grupo, materia, fecha/rango, unidad, estado de asistencia/justificante. |
| **Tutor** | Alumnos asignados, alertas, seguimientos, solicitudes. | Grupo, matrícula, porcentaje de asistencia, notas, severidad/estado de alerta y fecha. |
| **Alumno** | Solo materias, asistencia, calificaciones y solicitudes propias. | Periodo, materia, fechas, unidad y estado de asistencia/solicitud. |
| **Administrador** | Cuentas institucionales y usuarios. | Matrícula, correo, rol y estado; **sin acceso irrestricto** a expedientes. |

Las búsquedas y filtros se contemplan en el diseño funcional de cada rol.

## 13. Validación de la información

### 13.1 Validación frontend

| Campo | Regla | Comportamiento esperado |
|---|---|---|
| Matrícula | Obligatoria, 8 dígitos. | Marcar error antes de enviar si el formato no coincide. |
| Correo institucional | Obligatorio, patrón `23170202@uttcampus.edu.mx`. | Validar formato antes del envío. |
| Calificación | Número de 0.00 a 100.00, permite decimales. | Impedir/marcar valores fuera del rango. |
| Nueva contraseña | Obligatoria al cambiar la contraseña. | Exigir confirmación coincidente. |
| Evidencia | PDF/JPG/JPEG/PNG, máximo 10 MB. | Advertir sobre archivo prohibido o excedido. |
| Motivo/Descripción | Obligatorios según la solicitud del diseño. | Impedir envío incompleto. |
| Retardo configurable | Número entero positivo. | Validar rango; sugerir 15 minutos. |
| Retardos por falta | Número entero positivo. | Validar rango; sugerir 3. |

### 13.2 Validación backend

| Mecanismo | Regla | Objetivo |
|---|---|---|
| Datos únicos | Matrícula y correo no se duplican. | Identidad consistente. |
| Calificaciones | 0.00–100.00 y categoría NA/SA/DE/AU sin redondear. | Evitar clasificación inconsistente. |
| Autorización | Validar rol y relación con cada recurso. | Evitar accesos ajenos. |
| Asistencia | Verificar inscripción, sesión activa, duplicados y red. | Impedir registros inválidos. |
| Justificantes | Solicitud ligada a una falta real y propia. | No justificar eventos inexistentes. |
| Archivos | Revalidar tipo/extensión y tamaño en servidor. | Evitar manipulación de validación del cliente. |
| Fechas | Orden lógico de periodos, sesiones y plazos. | Cronología coherente. |
| Persistencia | Transacciones y restricciones relacionales. | Reducir inconsistencias por fallos parciales. |

## 14. Definición de contenido (textos, imágenes, etc.)

### 14.1 Inventario de contenido

| Tipo | Decisión de diseño | Estado |
|---|---|---|
| Textos funcionales | Módulos, etiquetas, estados, alertas, errores e instrucciones. | Definido funcionalmente. |
| Logo institucional | Logo UTT en acceso/documentación y espacios autorizados. | Disponible; uso sujeto a indicaciones. |
| Colores | Paleta Figma EDUTRAK. | Definida en prototipo; validación institucional pendiente. |
| Tipografía | Inter, con jerarquía documentada. | Fuente propuesta; validar identidad institucional. |
| Iconografía | Lucide; respetar atribuciones/licencias de recursos. | Biblioteca propuesta. |
| Imágenes/fotografías | Solo si aportan contexto y se acredita origen. | Parcial. |
| Evidencias del Alumno | PDF/JPG/JPEG/PNG, máximo 10 MB. | Privadas y accesibles según rol. |
| Mensajes de error | Diseños 403, 404, error general, estados vacío/cargando/éxito y validaciones. | Representados en prototipo. |

### 14.2 Sistema visual propuesto en Figma

| Elemento | Valor |
|---|---|
| Primario | `#029A7B` |
| Claro | `#6CC8B4` |
| Énfasis (burgundy) | `#8A2D40` |
| Fondo | `#F6F8F8` |
| Superficie | `#FFFFFF` |
| Texto | `#17201F` |
| Borde | `#D9E3E0` |
| Tipografía de aplicación | **Inter** (SIL OFL 1.1), propuesta |
| H1 | **32 px**, negrita |
| H2 | **24 px**, seminegrita |
| Cuerpo | **14 px**, regular |
| Pie/caption | **12 px**, peso medio |
| Iconos | **Lucide**; uso conforme a su licencia |

La tipografía propuesta para la aplicación es **Inter**. La documentación académica utiliza **Arial 12**. Los elementos de identidad institucional se ajustarán al manual de la Universidad.

![Figura 24. Sistema de diseño de EDUTRAK](assets/unidad1/fig-24.jpg)

*Figura 24. Sistema visual propuesto en Figma: paleta, tipografía, botones, campos y estados.*

