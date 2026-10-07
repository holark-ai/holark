import type { ComponentProps } from 'react'
import { MenuItem, MenuPopover } from './Menu'
import styles from './SplitButtonMenu.module.css'

export function SplitButtonMenu({ className = '', ...props }: ComponentProps<typeof MenuPopover>) {
  return <MenuPopover {...props} className={`${styles.menu} ${className}`} />
}

export function SplitButtonMenuItem({ className = '', ...props }: ComponentProps<typeof MenuItem>) {
  return <MenuItem {...props} className={`${styles.item} ${className}`} />
}
