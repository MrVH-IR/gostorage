# HTTP API — قرارداد پیشنهادی نسخه 1.0

همه مسیرهای عمومی زیر روی Coordinator هستند. مسیرهای `/internal` فقط برای ارتباط Coordinator با Storage Node استفاده می‌شوند و نباید از اینترنت عمومی در دسترس باشند.

## Public API

### GET /api/v1/files

فهرست فایل‌های کامل و فعال مالک داخلی را برمی‌گرداند. پارامتر اختیاری `limit` بین ۱ تا ۱۰۰ است و مقدار پیش‌فرض آن ۵۰ است. پاسخ شامل `files` و `count` است؛ فایل‌های نیمه‌کاره و حذف‌شده نمایش داده نمی‌شوند.

نمونه:

```sh
curl -f 'http://localhost:8080/api/v1/files?limit=50'
```

### POST /api/v1/files

Upload یک فایل به صورت `multipart/form-data` با فیلد `file`.

رفتار:
- اندازه فایل باید بزرگ‌تر از صفر و کمتر از سقف پیکربندی باشد.
- حداقل دو Node واجدشرایط لازم است.
- پاسخ فقط پس از ذخیره تمام Chunkها موفق اعلام می‌شود.

پاسخ موفق `201 Created`:

```json
{
  "id": "file-uuid",
  "name": "example.bin",
  "size_bytes": 10485760,
  "chunks": 3,
  "sha256": "hex-encoded-sha256",
  "status": "active"
}
```

خطاهای مهم:
- `400` ورودی نامعتبر
- `413` فایل بزرگ‌تر از حد مجاز
- `503` تعداد Node سالم کافی نیست
- `502` خطای Storage Node
- `500` خطای داخلی

### GET /api/v1/files/{fileID}

دریافت Metadata فایل. فایل حذف‌شده یا متعلق به مالک دیگر نباید افشا شود.

### GET /api/v1/files/{fileID}/content

Download محتوای اصلی با ترتیب Chunkها. فقط فایل active قابل دریافت است؛ پیش از ارسال، کل فایل در فایل موقت بازسازی و checksum آن کنترل می‌شود.

### DELETE /api/v1/files/{fileID}

حذف فایل و تمام Chunkهای آن. عملیات باید idempotent باشد.

## Internal Storage Node API

### POST /internal/v1/nodes/heartbeat

ثبت Node و به‌روزرسانی heartbeat با Header `X-Internal-Token`. Body شامل `id`, `name`, `base_url` و `capacity_bytes` است. Node باید UUID ثابت داشته باشد و آدرس `base_url` از Coordinator قابل دسترس باشد.

### GET /internal/v1/health

پاسخ:

```json
{
  "status": "ok"
}
```

### PUT /internal/v1/chunks/{chunkID}

Headerها:
- `X-Storage-Key`: شناسه داخلی ذخیره‌سازی
- `X-Chunk-SHA256`: checksum مورد انتظار
- `X-Chunk-Size`: اندازه مورد انتظار
- `X-Internal-Token`: احراز هویت داخلی

Body: بایت‌های خام Chunk، به شکل Stream.

Node باید اندازه و checksum واقعی را کنترل کند و فقط پس از موفقیت، Chunk را Commit کند.

### GET /internal/v1/chunks/{chunkID}

Header احراز هویت داخلی لازم است. Body بایت‌های Chunk به شکل Stream.

### DELETE /internal/v1/chunks/{chunkID}

حذف Chunk بر اساس شناسه/کلید داخلی. حذف دوباره باید موفق و بی‌خطر باشد.

## قواعد API

- پاسخ‌ها JSON هستند، به جز Body دانلود و PUT Chunk.
- برای درخواست‌ها Timeout تعریف شود.
- شناسه‌های داخلی UUID باشند.
- نام فایل و Content-Type ورودی کاربر قابل اعتماد نیستند.
- در پاسخ خطا، جزئیات داخلی Node، مسیر Filesystem و Secretها ارسال نشوند.
