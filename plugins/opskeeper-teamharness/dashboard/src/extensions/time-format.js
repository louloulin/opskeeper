const beijingFormatter = new Intl.DateTimeFormat('zh-CN', {
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

function format(value, formatter) {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  const part = (type) => formatter.formatToParts(date).find((item) => item.type === type)?.value || '';
  const dateText = part('year') ? `${part('year')}-${part('month')}-${part('day')} ` : '';
  return `${dateText}${part('hour')}:${part('minute')}:${part('second')}`;
}

export function formatBeijingTime(value) {
  const formatted = format(value, beijingFormatter);
  return formatted ? `${formatted} 北京时间` : value;
}

export function formatBeijingClock(value) {
  const formatted = format(value, beijingClockFormatter);
  return formatted ? `${formatted} 北京时间` : value;
}

export function formatBeijingTimeWithUtc(value) {
  const beijing = format(value, beijingFormatter);
  const utc = format(value, utcFormatter);
  if (!beijing || !utc) return value;
  return `${beijing} 北京时间 / ${utc} UTC`;
}
