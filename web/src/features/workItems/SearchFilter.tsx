import type { ReactNode } from 'react'
import * as Select from '@radix-ui/react-select'
import { Check, ChevronDown } from 'lucide-react'
import styles from './SearchFilter.module.css'

export type SearchFilterOption = readonly [value: string, label: string, content?: ReactNode]

type SearchFilterProps = {
  label: string
  value: string
  options: ReadonlyArray<SearchFilterOption>
  onValueChange: (value: string) => void
  active?: boolean
  className?: string
}

// Radix reserves an empty value for its placeholder; the people filters use it for Anyone.
const emptyValue = '__anyone__'

export function SearchFilter({ label, value, options, onValueChange, active = false, className = '' }: SearchFilterProps) {
  return (
    <Select.Root value={value || emptyValue} onValueChange={(next) => onValueChange(next === emptyValue ? '' : next)}>
      <Select.Trigger className={`${styles.trigger} ${className}`} aria-label={label} data-active={active}>
        <span className={styles.label}>{label}</span>
        <span className={styles.value}><Select.Value /></span>
        <Select.Icon asChild><ChevronDown aria-hidden="true" /></Select.Icon>
      </Select.Trigger>
      <Select.Portal>
        <Select.Content className={styles.content} position="popper" sideOffset={6} align="start" collisionPadding={8}>
          <Select.Viewport className={styles.viewport}>
            {options.map(([option, text, content]) => (
              <Select.Item className={styles.option} key={option} value={option || emptyValue} textValue={text.replace(/^@/, '')}>
                <Select.ItemText>{content ?? text}</Select.ItemText>
                <Select.ItemIndicator className={styles.indicator}><Check aria-hidden="true" /></Select.ItemIndicator>
              </Select.Item>
            ))}
          </Select.Viewport>
        </Select.Content>
      </Select.Portal>
    </Select.Root>
  )
}
