package parameters

// Budget removes the null bitmap and type bytes of count parameters from base.
// base must already exclude the fixed command header and any encoded SQL.
func Budget(base int64, count int) int64 {
	if count == 0 {
		return base
	}
	return base - int64((count+7)/8+1+2*count)
}
