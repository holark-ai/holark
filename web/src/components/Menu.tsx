import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from 'react'
import styles from './Menu.module.css'

export function MenuPopover({ children, className = '', ...props }: { children: ReactNode; className?: string } & HTMLAttributes<HTMLDivElement>) {
  return <div {...props} className={`${styles.popover} ${className}`} role="menu">{children}</div>
}

export function MenuItem({ children, className = '', destructive = false, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { destructive?: boolean }) {
  return (
    <button
      {...props}
      className={`${styles.item} ${destructive ? styles.destructive : ''} ${className}`}
      role="menuitem"
      type="button"
    >
      {children}
    </button>
  )
}
