const beijingTimeFormatter = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai',
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
});

const beijingClockFormatter = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
});

const utcFormatter = new Intl.DateTimeFormat('en-GB', {
  timeZone: 'UTC',
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hour12: false,
});

function format(value: string | number | Date, formatter: Intl.DateTimeFormat) {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  const part = (type: Intl.DateTimeFormatPartTypes) =>
    formatter.formatToParts(date).find((item) => item.type === type)?.value ?? '';
  const dateText = part('year') ? `${part('year')}-${part('month')}-${part('day')} ` : '';
  return `${dateText}${part('hour')}:${part('minute')}:${part('second')}`;
}

export function formatBeijingTime(value: string | number | Date) {
  const formatted = format(value, beijingTimeFormatter);
  return formatted ? `${formatted} 北京时间` : '';
}

export function formatBeijingClock(value: string | number | Date) {
  const formatted = format(value, beijingClockFormatter);
  return formatted ? `${formatted} 北京时间` : '';
}

export function formatBeijingTimeWithUtc(value: string | number | Date) {
  const beijing = format(value, beijingTimeFormatter);
  const utc = format(value, utcFormatter);
  if (!beijing || !utc) return '';
  return `${beijing} 北京时间 / ${utc} UTC`;
}
