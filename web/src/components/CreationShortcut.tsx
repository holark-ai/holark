import styles from './CreationShortcut.module.css'

export function CreationShortcut({ letter }: { letter: 'N' | 'T' }) {
  const isMac = /Mac|iPhone|iPad|iPod/i.test(navigator.platform)
  return <kbd className={styles.chip} aria-hidden="true" title={`Shift + ${isMac ? 'Option' : 'Alt'} + ${letter}`}>⇧ {isMac ? '⌥' : 'Alt'} {letter}</kbd>
}
