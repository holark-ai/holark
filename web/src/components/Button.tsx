import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react'
import styles from './Button.module.css'

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'primary' | 'secondary' | 'ai' | 'ghost'
  size?: 'small' | 'medium'
  icon?: ReactNode
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'medium', icon, children, className = '', ...props },
  ref,
) {
  return (
    <button ref={ref} className={`${styles.button} ${styles[variant]} ${styles[size]} ${className}`} data-variant={variant} type="button" {...props}>
      {icon}
      {children}
    </button>
  )
})
