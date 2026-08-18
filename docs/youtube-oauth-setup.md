# YouTube OAuth — Настройка Device Flow

> Позволяет пользователям привязать YouTube-аккаунт через Telegram-бота и смотреть **подписки** и **плейлисты** прямо в Lampa.

---

## Содержание

1. [Создание проекта Google Cloud](#1-создание-проекта-google-cloud)
2. [Включение YouTube Data API v3](#2-включение-youtube-data-api-v3)
3. [Настройка OAuth Consent Screen](#3-настройка-oauth-consent-screen)
4. [Создание OAuth Client ID](#4-создание-oauth-client-id)
5. [Конфигурация lampac-go](#5-конфигурация-lampac-go)
6. [Привязка аккаунта](#6-привязка-аккаунта)
7. [Квоты и лимиты](#7-квоты-и-лимиты)
8. [FAQ](#8-faq)

---

## 1. Создание проекта Google Cloud

1. Откройте [Google Cloud Console](https://console.cloud.google.com/)
2. В верхней панели нажмите **Select a project** → **New Project**
3. Заполните:
   - **Project name** — любое, например `Lampac YouTube`
   - **Organization** — оставьте по умолчанию (или `No organization`)
4. Нажмите **Create** и дождитесь создания
5. Убедитесь, что новый проект выбран в верхней панели

---

## 2. Включение YouTube Data API v3

1. В боковом меню перейдите в **APIs & Services** → **Library**
2. В строке поиска введите `YouTube Data API v3`
3. Кликните на карточку **YouTube Data API v3**
4. Нажмите **Enable**
5. Дождитесь активации (обычно мгновенно)

---

## 3. Настройка OAuth Consent Screen

1. Перейдите в **APIs & Services** → **OAuth consent screen**
2. Выберите тип **External** → нажмите **Create**
3. Заполните обязательные поля:

   | Поле | Значение |
   |------|----------|
   | App name | `Lampac` (или любое) |
   | User support email | ваш email |
   | Developer contact email | ваш email |

4. Нажмите **Save and Continue**

### Scopes

5. Нажмите **Add or Remove Scopes**
6. В поиске введите `youtube` и отметьте:
   ```
   https://www.googleapis.com/auth/youtube.readonly
   ```
7. Нажмите **Update** → **Save and Continue**

### Test Users

8. Нажмите **Add Users**
9. Введите **email Google-аккаунтов**, которые будут пользоваться функцией
10. Нажмите **Add** → **Save and Continue**

> [!IMPORTANT]
> Пока приложение в статусе **Testing**, авторизоваться смогут **только** добавленные тестовые пользователи (до 100 человек). Для личного сервера этого достаточно. Для снятия ограничения потребуется верификация Google (**Publish App**).

---

## 4. Создание OAuth Client ID

1. Перейдите в **APIs & Services** → **Credentials**
2. Нажмите **Create Credentials** → **OAuth client ID**
3. Заполните:

   | Поле | Значение |
   |------|----------|
   | Application type | **TVs and Limited Input devices** |
   | Name | `Lampac Device Flow` (или любое) |

4. Нажмите **Create**
5. В появившемся окне скопируйте:
   - **Client ID** — строка вида `123456789-xxxxxxx.apps.googleusercontent.com`
   - **Client Secret** — строка вида `GOCSPX-xxxxxxxxxxxxx`

> [!NOTE]
> Тип **TVs and Limited Input devices** — обязателен. Именно он включает Google Device Flow, который позволяет авторизоваться вводом кода на отдельном устройстве (как Smart TV).

---

## 5. Конфигурация lampac-go

Добавьте в `config.toml`:

```toml
[youtube_oauth]
client_id = "123456789-xxxxxxx.apps.googleusercontent.com"
client_secret = "GOCSPX-xxxxxxxxxxxxx"
```

Перезапустите сервер:

```bash
systemctl restart lampac
```

---

## 6. Привязка аккаунта

### Через Telegram-бота

| Команда | Описание |
|---------|----------|
| `/youtube_auth` | Начать привязку YouTube-аккаунта |
| `/youtube_unbind` | Отвязать аккаунт |

### Процесс привязки

1. Отправьте боту `/youtube_auth`
2. Бот ответит кодом и ссылкой:
   ```
   Перейдите на google.com/device и введите код: ABCD-EFGH
   ```
3. Откройте [google.com/device](https://google.com/device) на любом устройстве
4. Введите код из сообщения бота
5. Авторизуйтесь нужным Google-аккаунтом
6. Разрешите доступ приложению `Lampac`
7. Бот подтвердит привязку и покажет название канала

### Результат

После привязки в Lampa появится строка **«Подписки»** в ленте с последними видео из подписок пользователя.

**Доступные эндпоинты:**
- `youtube/feed/subscriptions` — последние видео из подписок
- `youtube/feed/playlists` — плейлисты канала
- `youtube/feed/playlist?playlist_id=X` — видео из конкретного плейлиста

---

## 7. Квоты и лимиты

| Параметр | Значение |
|----------|----------|
| Бесплатная квота | **10 000 единиц/день** |
| Одна сессия пользователя | ~5–10 единиц |
| Кэш ответов | 5 минут |
| Макс. пользователей (Testing) | 100 |

Для личного сервера (до 50 активных пользователей) бесплатной квоты хватает с большим запасом.

---

## 8. FAQ

**Q: Пользователь не может авторизоваться, ошибка «Access blocked»**
A: Убедитесь, что email пользователя добавлен в **Test Users** (шаг 3.8). Пока приложение в режиме Testing, другие аккаунты не смогут войти.

**Q: Токен перестал работать**
A: Refresh-токен обновляется автоматически. Если Google отозвал доступ (пользователь убрал разрешение в настройках аккаунта), нужно повторить `/youtube_auth`.

**Q: Где хранятся токены?**
A: В файле `database/ytauth/tokens.json`. Каждый пользователь идентифицируется по Telegram ID.

**Q: Можно ли использовать без Telegram-бота?**
A: Нет, привязка аккаунта работает только через Telegram-бота. Бот должен быть настроен в секции `[telegram]`.
