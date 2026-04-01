package checkout

import (
	"context"
	"fmt"
	"sync"

	"checkout-service/internal/money"
	pb "checkout-service/proto"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var log = logrus.New()

// Service implements hipstershop.CheckoutServiceServer.
type Service struct {
	pb.UnimplementedCheckoutServiceServer

	ProductCatalogConn *grpc.ClientConn
	CartConn           *grpc.ClientConn
	CurrencyConn       *grpc.ClientConn
	ShippingConn       *grpc.ClientConn
	PaymentConn        *grpc.ClientConn
	EmailConn          *grpc.ClientConn
}

func (s *Service) PlaceOrder(ctx context.Context, req *pb.PlaceOrderRequest) (*pb.PlaceOrderResponse, error) {
	log.Infof("[PlaceOrder] user_id=%q user_currency=%q", req.UserId, req.UserCurrency)

	orderID, err := uuid.NewUUID()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate order uuid")
	}

	prep, err := s.prepareOrderItemsAndShippingQuote(ctx, req.UserId, req.UserCurrency, req.Address)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}

	total := &pb.Money{CurrencyCode: req.UserCurrency, Units: 0, Nanos: 0}
	total = money.Must(money.Sum(total, prep.shippingCostLocalized))
	for _, it := range prep.orderItems {
		multPrice := money.MultiplySlow(it.Cost, uint32(it.GetItem().GetQuantity()))
		total = money.Must(money.Sum(total, multPrice))
	}

	txID, err := s.chargeCard(ctx, total, req.CreditCard)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to charge card: %+v", err)
	}
	log.Infof("payment went through (transaction_id: %s)", txID)

	shippingTrackingID, err := s.shipOrder(ctx, req.Address, prep.cartItems)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "shipping error: %+v", err)
	}

	_ = s.emptyUserCart(ctx, req.UserId)

	orderResult := &pb.OrderResult{
		OrderId:            orderID.String(),
		ShippingTrackingId: shippingTrackingID,
		ShippingCost:       prep.shippingCostLocalized,
		ShippingAddress:    req.Address,
		Items:              prep.orderItems,
	}

	if err := s.sendOrderConfirmation(ctx, req.Email, orderResult); err != nil {
		log.Warnf("failed to send order confirmation to %q: %+v", req.Email, err)
	} else {
		log.Infof("order confirmation email sent to %q", req.Email)
	}

	return &pb.PlaceOrderResponse{Order: orderResult}, nil
}

// --- internal helpers ---

type orderPrep struct {
	orderItems            []*pb.OrderItem
	cartItems             []*pb.CartItem
	shippingCostLocalized *pb.Money
}

func (s *Service) prepareOrderItemsAndShippingQuote(ctx context.Context, userID, userCurrency string, address *pb.Address) (orderPrep, error) {
	var out orderPrep

	cartItems, err := s.getUserCart(ctx, userID)
	if err != nil {
		return out, fmt.Errorf("cart failure: %w", err)
	}

	orderItems, err := s.prepOrderItems(ctx, cartItems, userCurrency)
	if err != nil {
		return out, fmt.Errorf("failed to prepare order: %w", err)
	}

	shippingUSD, err := s.quoteShipping(ctx, address, cartItems)
	if err != nil {
		return out, fmt.Errorf("shipping quote failure: %w", err)
	}

	shippingPrice, err := s.convertCurrency(ctx, shippingUSD, userCurrency)
	if err != nil {
		return out, fmt.Errorf("failed to convert shipping cost: %w", err)
	}

	out.shippingCostLocalized = shippingPrice
	out.cartItems = cartItems
	out.orderItems = orderItems
	return out, nil
}

func (s *Service) getUserCart(ctx context.Context, userID string) ([]*pb.CartItem, error) {
	cart, err := pb.NewCartServiceClient(s.CartConn).GetCart(ctx, &pb.GetCartRequest{UserId: userID})
	if err != nil {
		return nil, fmt.Errorf("failed to get user cart: %w", err)
	}
	return cart.GetItems(), nil
}

func (s *Service) emptyUserCart(ctx context.Context, userID string) error {
	_, err := pb.NewCartServiceClient(s.CartConn).EmptyCart(ctx, &pb.EmptyCartRequest{UserId: userID})
	if err != nil {
		return fmt.Errorf("failed to empty user cart: %w", err)
	}
	return nil
}

func (s *Service) prepOrderItems(ctx context.Context, items []*pb.CartItem, userCurrency string) ([]*pb.OrderItem, error) {
	type productResult struct {
		product *pb.Product
		err     error
	}

	products := make([]productResult, len(items))
	cl := pb.NewProductCatalogServiceClient(s.ProductCatalogConn)

	var wg sync.WaitGroup
	for i, item := range items {
		wg.Add(1)
		go func(i int, item *pb.CartItem) {
			defer wg.Done()
			p, err := cl.GetProduct(ctx, &pb.GetProductRequest{Id: item.GetProductId()})
			products[i] = productResult{p, err}
		}(i, item)
	}
	wg.Wait()

	type orderResult struct {
		item *pb.OrderItem
		err  error
	}

	out := make([]orderResult, len(items))
	for i, pr := range products {
		wg.Add(1)
		go func(i int, pr productResult) {
			defer wg.Done()
			if pr.err != nil {
				out[i] = orderResult{err: fmt.Errorf("failed to get product %q: %w", items[i].GetProductId(), pr.err)}
				return
			}
			price, err := s.convertCurrency(ctx, pr.product.GetPriceUsd(), userCurrency)
			if err != nil {
				out[i] = orderResult{err: fmt.Errorf("failed to convert price of %q to %s: %w", items[i].GetProductId(), userCurrency, err)}
				return
			}
			out[i] = orderResult{item: &pb.OrderItem{Item: items[i], Cost: price}}
		}(i, pr)
	}
	wg.Wait()

	result := make([]*pb.OrderItem, len(items))
	for i, r := range out {
		if r.err != nil {
			return nil, r.err
		}
		result[i] = r.item
	}
	return result, nil
}

func (s *Service) quoteShipping(ctx context.Context, address *pb.Address, items []*pb.CartItem) (*pb.Money, error) {
	resp, err := pb.NewShippingServiceClient(s.ShippingConn).GetQuote(ctx, &pb.GetQuoteRequest{
		Address: address,
		Items:   items,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get shipping quote: %w", err)
	}
	return resp.GetCostUsd(), nil
}

func (s *Service) convertCurrency(ctx context.Context, from *pb.Money, toCurrency string) (*pb.Money, error) {
	result, err := pb.NewCurrencyServiceClient(s.CurrencyConn).Convert(ctx, &pb.CurrencyConversionRequest{
		From:   from,
		ToCode: toCurrency,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to convert currency: %w", err)
	}
	return result, nil
}

func (s *Service) chargeCard(ctx context.Context, amount *pb.Money, paymentInfo *pb.CreditCardInfo) (string, error) {
	resp, err := pb.NewPaymentServiceClient(s.PaymentConn).Charge(ctx, &pb.ChargeRequest{
		Amount:     amount,
		CreditCard: paymentInfo,
	})
	if err != nil {
		return "", fmt.Errorf("could not charge the card: %w", err)
	}
	return resp.GetTransactionId(), nil
}

func (s *Service) sendOrderConfirmation(ctx context.Context, email string, order *pb.OrderResult) error {
	_, err := pb.NewEmailServiceClient(s.EmailConn).SendOrderConfirmation(ctx, &pb.SendOrderConfirmationRequest{
		Email: email,
		Order: order,
	})
	return err
}

func (s *Service) shipOrder(ctx context.Context, address *pb.Address, items []*pb.CartItem) (string, error) {
	resp, err := pb.NewShippingServiceClient(s.ShippingConn).ShipOrder(ctx, &pb.ShipOrderRequest{
		Address: address,
		Items:   items,
	})
	if err != nil {
		return "", fmt.Errorf("shipment failed: %w", err)
	}
	return resp.GetTrackingId(), nil
}
