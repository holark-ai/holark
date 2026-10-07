export const HOLON_TITLE_MAX_CHARACTERS = 80

export function limitHolonTitle(value: string) {
  const characters = [...value]
  return characters.length > HOLON_TITLE_MAX_CHARACTERS
    ? characters.slice(0, HOLON_TITLE_MAX_CHARACTERS).join('')
    : value
}
