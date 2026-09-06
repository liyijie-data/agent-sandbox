export function failure(code) {
  return Object.assign(new Error(code), {code});
}
